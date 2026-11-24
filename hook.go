package main

// Subcomando "hook": chamado pelos hooks do Claude Code, do Codex e pelo
// plugin do opencode a cada evento. Grava um registro pequeno por sessão em
// <stateDir>/agents, que o painel lê em /api/agents.
//
// Regras: nunca escreve no stdout (o stdout de alguns hooks entra no contexto
// do agente), sempre sai com 0 (um hook quebrado não pode travar o agente) e
// não grava prompt, transcript nem nada além do estado.

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	agentWorking = "working"
	agentWaiting = "waiting"
	agentDone    = "done"
	agentEnded   = "ended"

	agentRecordMaxAge = 7 * 24 * time.Hour
	hookStdinMax      = 64 << 10
)

var (
	sessionRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	knownAgents = map[string]bool{"claude": true, "codex": true, "opencode": true}
)

// AgentRecord é o que o hook grava e o painel lê.
type AgentRecord struct {
	Agent   string    `json:"agent"`
	Session string    `json:"session"`
	Cwd     string    `json:"cwd"`
	State   string    `json:"state"`
	Since   time.Time `json:"since"`
	TS      time.Time `json:"ts"`
	PID     int       `json:"pid,omitempty"`
}

// hookPayload junta os campos que interessam dos três agentes. O plugin do
// opencode manda "state" já resolvido.
type hookPayload struct {
	SessionID        string `json:"session_id"`
	Cwd              string `json:"cwd"`
	Event            string `json:"hook_event_name"`
	NotificationType string `json:"notification_type"`
	State            string `json:"state"`
}

// defaultStateDir: WT_STATE_DIR, senão $XDG_STATE_HOME/worktree-app, senão
// ~/.local/state/worktree-app. O hook roda sem config, então config e hook
// precisam chegar no mesmo lugar por esse caminho.
func defaultStateDir() string {
	if d := os.Getenv("WT_STATE_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "worktree-app")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "worktree-app")
	}
	return filepath.Join(home, ".local", "state", "worktree-app")
}

// hookState traduz o evento do agente no estado do painel; "" = ignorar.
func hookState(p hookPayload) string {
	switch p.State {
	case agentWorking, agentWaiting, agentDone, agentEnded:
		return p.State
	}
	switch p.Event {
	case "UserPromptSubmit", "PostToolUse":
		return agentWorking
	case "PermissionRequest":
		return agentWaiting
	case "Notification":
		switch p.NotificationType {
		case "idle_prompt":
			return agentDone
		case "", "permission_prompt", "elicitation_dialog":
			return agentWaiting
		}
		return ""
	case "Stop":
		return agentDone
	case "SessionEnd":
		return agentEnded
	}
	return ""
}

// runHook nunca devolve erro: qualquer falha só faz o registro não ser gravado.
func runHook(args []string, stdin io.Reader, now time.Time) {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	agent := fs.String("agent", "", "claude | codex | opencode")
	if fs.Parse(args) != nil || !knownAgents[*agent] {
		return
	}
	b, err := io.ReadAll(io.LimitReader(stdin, hookStdinMax))
	if err != nil {
		return
	}
	var p hookPayload
	if json.Unmarshal(b, &p) != nil || !sessionRe.MatchString(p.SessionID) {
		return
	}
	state := hookState(p)
	if state == "" {
		return
	}
	cwd := p.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if cwd == "" || !filepath.IsAbs(cwd) {
		return
	}
	dir := filepath.Join(defaultStateDir(), "agents")
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	rec := AgentRecord{Agent: *agent, Session: p.SessionID, Cwd: filepath.Clean(cwd), State: state, Since: now, TS: now, PID: agentAncestor(os.Getppid(), *agent)}
	path := filepath.Join(dir, *agent+"-"+p.SessionID+".json")
	if old, ok := readAgentRecord(path); ok {
		if old.State == rec.State && old.Cwd == rec.Cwd && (rec.PID == 0 || old.PID == rec.PID) {
			return // nada mudou: não regrava
		}
		if old.State == rec.State {
			rec.Since = old.Since
		}
		if rec.PID == 0 {
			rec.PID = old.PID
		}
	}
	_ = writeFileAtomic(path, mustJSON(rec), 0o600)
	pruneAgentRecords(dir, now)
}

func readAgentRecord(path string) (AgentRecord, bool) {
	var r AgentRecord
	b, err := os.ReadFile(path)
	if err != nil || len(b) > hookStdinMax || json.Unmarshal(b, &r) != nil {
		return r, false
	}
	return r, knownAgents[r.Agent] && sessionRe.MatchString(r.Session) && filepath.IsAbs(r.Cwd)
}

func pruneAgentRecords(dir string, now time.Time) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > agentRecordMaxAge {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr == nil && cerr == nil {
		werr = os.Chmod(name, perm)
	}
	if werr == nil && cerr == nil {
		if werr = os.Rename(name, path); werr == nil {
			return nil
		}
	}
	_ = os.Remove(name)
	if werr != nil {
		return werr
	}
	return cerr
}

// procRoot é trocado nos testes.
var procRoot = "/proc"

// agentComm diz se o nome do processo é o do agente (o comm do Linux corta
// em 15 caracteres; o Claude às vezes aparece como "node" com claude no argv).
func agentComm(pid int, agent string) bool {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "comm"))
	if err != nil {
		return false
	}
	comm := strings.TrimSpace(string(b))
	if comm == agent {
		return true
	}
	if comm != "node" && comm != "bun" {
		return false
	}
	cl, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}
	for _, a := range strings.Split(string(cl), "\x00") {
		if filepath.Base(a) == agent {
			return true
		}
	}
	return false
}

func parentPID(pid int) int {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0
	}
	// "pid (comm) state ppid ..." — o comm pode ter espaço e parêntese.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(f[1])
	return n
}

// agentAncestor sobe a árvore a partir de pid até achar o processo do agente.
func agentAncestor(pid int, agent string) int {
	for i := 0; i < 12 && pid > 1; i++ {
		if agentComm(pid, agent) {
			return pid
		}
		pid = parentPID(pid)
	}
	return 0
}
