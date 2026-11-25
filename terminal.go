package main

// Botões "Terminal" e "Agente" do card: abrem uma aba nova (Windows Terminal
// por padrão) já na pasta da worktree, com o agente rodando ou retomando a
// última sessão registrada pelo hook.
//
// Tudo é argv: nada passa por cmd.exe. Como o wt.exe usa ";" como separador
// e o wsl.exe repassa argumentos para um shell, cada argumento precisa casar
// com argRe (sem espaço, aspas, ";" ou "&"). O comando do agente roda pelo
// subcomando "run", que monta a linha do shell do lado Linux.

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// AgentLaunch diz como abrir um agente novo e como retomar uma sessão.
type AgentLaunch struct {
	New    []string `json:"new"`
	Resume []string `json:"resume,omitempty"`
}

var argRe = regexp.MustCompile(`^[A-Za-z0-9._/:=@+,-]+$`)

func defaultAgents() map[string]AgentLaunch {
	return map[string]AgentLaunch{
		"claude":   {New: []string{"claude"}, Resume: []string{"claude", "--resume", "{session}"}},
		"codex":    {New: []string{"codex"}, Resume: []string{"codex", "resume", "{session}"}},
		"opencode": {New: []string{"opencode"}, Resume: []string{"opencode", "-s", "{session}"}},
	}
}

// defaultTerminal só liga o recurso onde ele foi pensado: WSL com distro
// conhecida (aba do Windows Terminal entrando no WSL).
func defaultTerminal(distro string) []string {
	if runtime.GOOS == "linux" && distro != "" {
		return []string{"wt.exe", "-w", "0", "nt", "--title", "{title}", "wsl.exe", "-d", "{distro}", "--cd", "{path}", "-e", "{cmd}"}
	}
	return nil
}

func (c *Config) validateTerminal() error {
	if len(c.Terminal) == 0 {
		return nil
	}
	hasCmd := false
	for _, a := range c.Terminal {
		if a == "{cmd}" {
			hasCmd = true
		}
	}
	if !hasCmd {
		return errors.New(`terminal precisa ter "{cmd}" como argumento`)
	}
	for name, a := range c.Agents {
		if !argRe.MatchString(name) || len(a.New) == 0 {
			return fmt.Errorf("agents.%s precisa de \"new\"", name)
		}
		for _, x := range append(append([]string{}, a.New...), a.Resume...) {
			if x != "{session}" && !argRe.MatchString(x) {
				return fmt.Errorf("agents.%s: argumento %q não permitido (sem espaço, aspas, ; ou &)", name, x)
			}
		}
	}
	return nil
}

// terminalArgv expande o modelo. Placeholder só vale como argumento inteiro;
// {cmd} vira vários argumentos.
func terminalArgv(tmpl []string, vars map[string]string, cmd []string) ([]string, error) {
	var out []string
	for _, a := range tmpl {
		switch a {
		case "{cmd}":
			out = append(out, cmd...)
		case "{title}", "{path}", "{distro}":
			out = append(out, vars[a])
		default:
			out = append(out, a)
		}
	}
	for _, a := range out {
		if !argRe.MatchString(a) {
			return nil, fmt.Errorf("argumento não permitido para o terminal: %q", a)
		}
	}
	return out, nil
}

var titleRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safeTitle(s string) string {
	s = strings.Trim(titleRe.ReplaceAllString(s, "-"), "-")
	if s == "" {
		return "worktree"
	}
	return s
}

// lastSession devolve a sessão mais recente do agente nessa worktree.
func lastSession(entries []wtEntry, recs []AgentRecord, agent, path string) string {
	best := AgentRecord{}
	for _, r := range recs {
		if r.Agent != agent {
			continue
		}
		if e, ok := matchWorktree(entries, r.Cwd); ok && samePath(e.Path, path) && r.TS.After(best.TS) {
			best = r
		}
	}
	return best.Session
}

// resumableAgents diz, por worktree (path), quais agentes têm sessão para
// retomar: o mesmo critério do lastSession (registro mais recente do agente
// naquela worktree, com sessão), numa passada só pelos registros. Agente sem
// "resume" configurado nunca entra. Nunca devolve nil (vira {} no JSON).
func resumableAgents(entries []wtEntry, recs []AgentRecord, agents map[string]AgentLaunch) map[string][]string {
	type key struct{ agent, path string }
	best := map[key]AgentRecord{}
	for _, r := range recs {
		if a, ok := agents[r.Agent]; !ok || len(a.Resume) == 0 {
			continue
		}
		e, ok := matchWorktree(entries, r.Cwd)
		if !ok {
			continue
		}
		k := key{r.Agent, e.Path}
		if b := best[k]; r.TS.After(b.TS) {
			best[k] = r
		}
	}
	out := map[string][]string{}
	for k, r := range best {
		if r.Session != "" {
			out[k.path] = append(out[k.path], k.agent)
		}
	}
	for _, names := range out {
		sort.Strings(names)
	}
	return out
}

func defaultLaunch(argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func (s *server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string `json:"path"`
		Agent  string `json:"agent"`
		Resume bool   `json:"resume"`
	}
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(s.cfg.Terminal) == 0 {
		http.Error(w, "terminal não configurado", http.StatusBadRequest)
		return
	}
	entries := s.wts.get(r.Context(), s.cfg)
	p := filepath.Clean(filepath.FromSlash(req.Path))
	e, ok := matchWorktree(entries, p)
	if !ok || !samePath(e.Path, p) || !within(s.cfg.Root, p) {
		http.Error(w, "caminho não é uma worktree conhecida", http.StatusBadRequest)
		return
	}
	self, err := os.Executable()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cmd := []string{self, "run"}
	title := e.Feature
	if e.Main {
		title = e.Folder
	}
	if req.Agent != "" {
		al, ok := s.cfg.Agents[req.Agent]
		if !ok {
			http.Error(w, "agente não configurado", http.StatusBadRequest)
			return
		}
		args := al.New
		if req.Resume {
			sess := lastSession(entries, loadAgentRecords(s.agentsDir(), time.Now()), req.Agent, e.Path)
			if sess == "" || len(al.Resume) == 0 {
				http.Error(w, "nenhuma sessão para retomar nesta worktree", http.StatusBadRequest)
				return
			}
			args = nil
			for _, a := range al.Resume {
				args = append(args, strings.ReplaceAll(a, "{session}", sess))
			}
		}
		cmd = append(cmd, args...)
		title = req.Agent + "-" + title
	}
	argv, err := terminalArgv(s.cfg.Terminal, map[string]string{"{title}": safeTitle(title), "{path}": e.Path, "{distro}": s.cfg.Distro}, cmd)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.launch(argv); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (c *Config) agentNames() []string {
	names := make([]string, 0, len(c.Agents))
	for n := range c.Agents {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
