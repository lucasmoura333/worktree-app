package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Tela de diagnóstico: junta o estado atual (worktrees, branches, portas,
// snapshots, sessões) com o histórico de operações para responder "o que está
// fora do lugar?" sem abrir terminal. Só lê; nada é alterado.
const diagCacheTTL = 20 * time.Second

type DiagItem struct {
	Severity string `json:"severity"` // error | warn | info
	Repo     string `json:"repo,omitempty"`
	Feature  string `json:"feature,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Path     string `json:"path,omitempty"`
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

type DiagCheck struct {
	ID    string     `json:"id"`
	Title string     `json:"title"`
	Desc  string     `json:"desc,omitempty"`
	Items []DiagItem `json:"items"`
}

type DiagReport struct {
	At       time.Time   `json:"at"`
	Busy     bool        `json:"busy,omitempty"`
	Errors   int         `json:"errors"`
	Warnings int         `json:"warnings"`
	Infos    int         `json:"infos"`
	Checks   []DiagCheck `json:"checks"`
}

var diagChecks = []struct{ id, title, desc string }{
	{"worktrees", "Worktrees", "Registros, pastas e arquivos copiados."},
	{"branches", "Branches órfãs", "Branches locais que nenhuma worktree usa."},
	{"ports", "Portas", "Portas do docker repetidas ou fora da faixa."},
	{"snapshots", "Snapshots", "Integridade dos snapshots guardados."},
	{"sessions", "Sessões de agente", "Sessões vivas, órfãs e sem hook."},
	{"history", "Histórico de operações", "Falhas registradas em ops.jsonl."},
}

func (s *server) handleDiagnostico(w http.ResponseWriter, r *http.Request) {
	fresh := r.URL.Query().Get("fresh") == "1"
	writeJSON(w, s.diagnostics(r.Context(), fresh))
}

// diagnostics devolve o relatório por alguns segundos: a tela atualiza a cada
// operação e a verificação de snapshots lê os arquivos inteiros.
func (s *server) diagnostics(ctx context.Context, fresh bool) DiagReport {
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	if !fresh && s.diagLast != nil && time.Since(s.diagAt) < diagCacheTTL {
		return *s.diagLast
	}
	busy := !s.opMu.TryLock()
	if !busy {
		s.opMu.Unlock()
	}
	rep := buildDiagnostics(ctx, s, busy)
	s.diagLast, s.diagAt = &rep, time.Now()
	return rep
}

func buildDiagnostics(ctx context.Context, s *server, busy bool) DiagReport {
	st := scanState(ctx, s.cfg)
	rep := DiagReport{At: time.Now(), Busy: busy}
	for _, c := range diagChecks {
		rep.Checks = append(rep.Checks, DiagCheck{ID: c.id, Title: c.title, Desc: c.desc, Items: []DiagItem{}})
	}
	rep.Checks[0].Items = diagWorktrees(s.cfg, st)
	rep.Checks[1].Items = diagBranches(ctx, s.cfg)
	rep.Checks[2].Items = diagPorts(s.cfg, st)
	if busy {
		rep.Checks[3].Items = []DiagItem{{Severity: "info", Title: "verificação adiada", Detail: "há uma operação em andamento", Hint: "verifique de novo quando ela terminar"}}
	} else {
		rep.Checks[3].Items = diagSnapshots(s.cfg)
	}
	rep.Checks[4].Items = diagSessions(ctx, s)
	rep.Checks[5].Items = diagHistory(s.cfg)
	for i := range rep.Checks {
		sortDiagItems(rep.Checks[i].Items)
		for _, it := range rep.Checks[i].Items {
			switch it.Severity {
			case "error":
				rep.Errors++
			case "warn":
				rep.Warnings++
			default:
				rep.Infos++
			}
		}
	}
	return rep
}

// sortDiagItems põe o que exige ação primeiro; a ordem dentro do mesmo nível
// fica estável (repo, feature).
func sortDiagItems(items []DiagItem) {
	rank := map[string]int{"error": 0, "warn": 1, "info": 2}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if rank[a.Severity] != rank[b.Severity] {
			return rank[a.Severity] < rank[b.Severity]
		}
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		return a.Feature < b.Feature
	})
}

func diagWorktrees(cfg *Config, st *State) []DiagItem {
	items := []DiagItem{}
	for _, rs := range st.Repos {
		if rs.Error != "" {
			items = append(items, DiagItem{Severity: "error", Repo: rs.Key, Title: "repo indisponível", Detail: rs.Error})
		}
	}
	for _, wt := range st.Stale {
		it := DiagItem{Repo: wt.Repo, Feature: wt.Feature, Branch: wt.Branch, Path: wt.Path}
		switch {
		case wt.Missing:
			it.Severity, it.Title = "error", "pasta da worktree sumiu"
			it.Detail = "o git ainda registra " + relSlash(cfg.Root, wt.Path)
			it.Hint = "use git worktree prune no card Registros quebrados"
		case wt.Prunable:
			it.Severity, it.Title = "warn", "registro prunável"
			it.Detail = "a pasta não existe mais"
			it.Hint = "use git worktree prune no card Registros quebrados"
		default:
			continue
		}
		items = append(items, it)
	}
	for _, f := range st.Features {
		for _, wt := range f.Worktrees {
			base := DiagItem{Repo: wt.Repo, Feature: wt.Feature, Branch: wt.Branch, Path: wt.Path}
			add := func(severity, title, detail, hint string) {
				it := base
				it.Severity, it.Title, it.Detail, it.Hint = severity, title, detail, hint
				items = append(items, it)
			}
			if wt.Error != "" {
				add("warn", "erro ao ler a worktree", wt.Error, "")
			}
			if len(wt.MissingFiles) > 0 {
				add("warn", "arquivos copiados ausentes", strings.Join(wt.MissingFiles, ", "), "use o preparo da worktree para copiar de novo")
			}
			if len(wt.MissingDeps) > 0 {
				add("warn", "dependências ausentes", strings.Join(wt.MissingDeps, ", "), "rode o preparo com instalar dependências")
			}
			if wt.Locked {
				add("info", "worktree trancada", "o git não a remove enquanto estiver assim", "destrave antes de remover")
			}
			if wt.Detached {
				add("info", "HEAD destacado", "sem branch para publicar", "crie ou troque para uma branch")
			}
			if wt.Branch != "" && wt.Feature != "" && !strings.Contains(wt.Branch, wt.Feature) {
				add("info", "branch ≠ pasta", "a pasta é "+wt.Folder+" e a branch é "+wt.Branch, "")
			}
		}
	}
	return items
}

func diagBranches(ctx context.Context, cfg *Config) []DiagItem {
	items := []DiagItem{}
	for i := range cfg.Repos {
		r := &cfg.Repos[i]
		main := cfg.mainPath(r)
		if !isDir(main) {
			continue
		}
		wts, err := listWorktrees(ctx, main)
		if err != nil {
			continue
		}
		checked := map[string]bool{}
		for _, g := range wts {
			if g.Branch != "" {
				checked[g.Branch] = true
			}
		}
		branches, err := listBranches(ctx, main)
		if err != nil {
			continue
		}
		tracks := branchTracks(ctx, main)
		for _, b := range branches {
			if b.Remote || b.Name == "" || checked[b.Name] || b.Name == r.Main {
				continue
			}
			t := tracks[b.Name]
			merged, _ := isAncestor(ctx, main, b.Name, r.Base)
			ahead := 0
			if !merged {
				ahead = countCommits(ctx, main, r.Base+".."+b.Name)
			}
			it := DiagItem{Repo: r.Key, Branch: b.Name}
			switch {
			case strings.Contains(t.Track, "gone"):
				it.Severity, it.Title = "warn", "upstream sumiu"
				it.Detail = t.Upstream + " não existe mais no remoto"
				it.Hint = "publique de novo (git push -u) ou apague a branch"
			case t.Upstream == "" && ahead > 0:
				it.Severity, it.Title = "warn", "commits só no local"
				it.Detail = fmt.Sprintf("%d commit(s) fora de %s, sem upstream", ahead, r.Base)
				it.Hint = "publique (git push -u origin " + b.Name + ") ou apague"
			case merged:
				it.Severity, it.Title = "info", "mergeada — pode apagar"
				it.Detail = "já está em " + r.Base + " e nenhuma worktree usa"
				it.Hint = "git branch -d " + b.Name
			default:
				continue
			}
			items = append(items, it)
		}
	}
	return items
}

type branchTrack struct{ Upstream, Track string }

func branchTracks(ctx context.Context, dir string) map[string]branchTrack {
	out, err := git(ctx, dir, "for-each-ref",
		"--format=%(refname:short)%09%(upstream:short)%09%(upstream:track)", "refs/heads")
	tracks := map[string]branchTrack{}
	if err != nil {
		return tracks
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		t := branchTrack{}
		if len(parts) > 1 {
			t.Upstream = parts[1]
		}
		if len(parts) > 2 {
			t.Track = parts[2]
		}
		tracks[parts[0]] = t
	}
	return tracks
}

// isAncestor separa "não é ancestral" (exit 1) de erro de verdade (base
// inexistente, por exemplo), que não deve virar diagnóstico falso.
func isAncestor(ctx context.Context, dir, a, b string) (bool, error) {
	err := command(ctx, dir, "git", "merge-base", "--is-ancestor", a, b).Run()
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func countCommits(ctx context.Context, dir, rng string) int {
	out, err := git(ctx, dir, "rev-list", "--count", rng)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}

func diagPorts(cfg *Config, st *State) []DiagItem {
	items := []DiagItem{}
	byPort := map[int][]WorktreeInfo{}
	for _, f := range st.Features {
		for _, wt := range f.Worktrees {
			if wt.Port > 0 {
				byPort[wt.Port] = append(byPort[wt.Port], wt)
			}
		}
	}
	ports := make([]int, 0, len(byPort))
	for p := range byPort {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	for _, p := range ports {
		list := byPort[p]
		if len(list) > 1 {
			labels := make([]string, len(list))
			for i, wt := range list {
				labels[i] = wt.Repo + "/" + wt.Feature
			}
			items = append(items, DiagItem{Severity: "error", Title: fmt.Sprintf("porta %d duplicada", p),
				Detail: strings.Join(labels, ", "), Hint: "duas worktrees subiriam o docker na mesma porta"})
		}
	}
	for _, f := range st.Features {
		for _, wt := range f.Worktrees {
			d := dockerEnv(cfg, wt.Repo)
			if d == nil || wt.Port <= 0 {
				continue
			}
			if wt.Port < d.PortMin || wt.Port > d.PortMax {
				items = append(items, DiagItem{Severity: "warn", Repo: wt.Repo, Feature: wt.Feature, Branch: wt.Branch,
					Title:  fmt.Sprintf("porta %d fora da faixa", wt.Port),
					Detail: fmt.Sprintf("a faixa configurada é %d–%d", d.PortMin, d.PortMax), Hint: "confira o .env da worktree"})
			}
		}
	}
	return items
}

func dockerEnv(cfg *Config, key string) *DockerEnvConfig {
	for i := range cfg.Repos {
		if cfg.Repos[i].Key == key {
			return cfg.Repos[i].DockerEnv
		}
	}
	return nil
}

func diagSnapshots(cfg *Config) []DiagItem {
	items := []DiagItem{}
	unsealed := 0
	for _, m := range listSnapshots(cfg, "", "") {
		checked, err := verifySnapshotFiles(m.Dir, m)
		if err != nil {
			items = append(items, DiagItem{Severity: "error", Repo: m.Repo, Feature: m.Folder, Branch: m.Branch,
				Title: "snapshot inválido", Detail: m.CreatedAt.Format("02/01 15:04") + " · " + err.Error(),
				Hint: "não restaure este snapshot; crie outro antes"})
			continue
		}
		if !checked {
			unsealed++
		}
	}
	if unsealed > 0 {
		items = append(items, DiagItem{Severity: "info", Title: fmt.Sprintf("%d snapshot(s) sem SHA256", unsealed),
			Detail: "criados antes da verificação de integridade", Hint: "sem ação: só não dá para conferir corrupção"})
	}
	return items
}

func diagSessions(ctx context.Context, s *server) []DiagItem {
	items := []DiagItem{}
	entries := s.wts.get(ctx, s.cfg)
	dir := s.agentsDir()
	recs := loadAgentRecords(dir, time.Now())
	ag := collectAgents(entries, recs, scanProcAgents(), dir)
	for _, r := range recs {
		if _, ok := matchWorktree(entries, r.Cwd); !ok {
			items = append(items, DiagItem{Severity: "info", Title: "sessão fora das worktrees",
				Detail: r.Agent + " · " + relSlash(s.cfg.Root, r.Cwd) + " · " + r.TS.Format("02/01 15:04"),
				Hint:   "a worktree foi removida ou o registro é de outra pasta"})
		}
	}
	paths := make([]string, 0, len(ag.LiveByPath))
	for path := range ag.LiveByPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if n := ag.LiveByPath[path]; n > 1 {
			items = append(items, DiagItem{Severity: "warn", Path: path,
				Title:  fmt.Sprintf("%d sessões vivas na mesma worktree", n),
				Detail: relSlash(s.cfg.Root, path), Hint: "duas sessões na mesma árvore se atrapalham; feche uma"})
		}
	}
	for _, a := range ag.Agents {
		if a.NoHook {
			items = append(items, DiagItem{Severity: "warn", Repo: a.Repo, Feature: a.Feature,
				Title: "agente rodando sem hook", Detail: a.Agent,
				Hint: "instale o hook para o painel acompanhar o estado"})
		}
	}
	return items
}

// readOpsLog lê o histórico inteiro (rotação inclusive) e devolve os últimos
// registros, do mais antigo para o mais novo.
func readOpsLog(cfg *Config, max int) []opRecord {
	root := stateRoot(cfg)
	var out []opRecord
	for _, name := range []string{opsFile + ".1", opsFile} {
		f, err := os.Open(filepath.Join(root, name))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			var r opRecord
			if json.Unmarshal(sc.Bytes(), &r) != nil {
				continue
			}
			out = append(out, r)
		}
		_ = f.Close()
	}
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}

func diagHistory(cfg *Config) []DiagItem {
	items := []DiagItem{}
	recs := readOpsLog(cfg, 200)
	shown := 0
	for i := len(recs) - 1; i >= 0 && shown < 20; i-- {
		r := recs[i]
		if r.OK {
			continue
		}
		shown++
		items = append(items, DiagItem{Severity: "warn", Title: r.Op + " falhou",
			Detail: r.Start.Format("02/01 15:04") + " · " + r.Error,
			Hint:   "veja a aba Atividade para o passo a passo"})
	}
	return items
}

// demoDiagnostics alimenta a prévia online: mesmo formato, dados de exemplo.
func demoDiagnostics() DiagReport {
	rep := DiagReport{At: time.Now()}
	for _, c := range diagChecks {
		rep.Checks = append(rep.Checks, DiagCheck{ID: c.id, Title: c.title, Desc: c.desc, Items: []DiagItem{}})
	}
	rep.Checks[0].Items = []DiagItem{{Severity: "warn", Repo: "app", Feature: "checkout-novo", Branch: "dev.checkout-novo.consolidado", Title: "arquivos copiados ausentes", Detail: ".env", Hint: "use o preparo da worktree para copiar de novo"}}
	rep.Checks[1].Items = []DiagItem{{Severity: "info", Repo: "app", Branch: "dev.checkout-poc", Title: "mergeada — pode apagar", Detail: "já está em origin/dev e nenhuma worktree usa", Hint: "git branch -d dev.checkout-poc"}}
	rep.Checks[2].Items = []DiagItem{{Severity: "error", Title: "porta 5184 duplicada", Detail: "app/checkout-novo, app/menu-lateral", Hint: "duas worktrees subiriam o docker na mesma porta"}}
	rep.Checks[3].Items = []DiagItem{{Severity: "info", Title: "2 snapshot(s) sem SHA256", Detail: "criados antes da verificação de integridade", Hint: "sem ação: só não dá para conferir corrupção"}}
	rep.Checks[4].Items = []DiagItem{{Severity: "info", Title: "sessão fora das worktrees", Detail: "claude · acme-app/dev.checkout-poc · 09/11 18:42", Hint: "a worktree foi removida ou o registro é de outra pasta"}}
	rep.Checks[5].Items = []DiagItem{{Severity: "warn", Title: "prune falhou", Detail: "10/11 08:15 · git worktree prune: pasta ocupada", Hint: "veja a aba Atividade para o passo a passo"}}
	for i := range rep.Checks {
		sortDiagItems(rep.Checks[i].Items)
		for _, it := range rep.Checks[i].Items {
			switch it.Severity {
			case "error":
				rep.Errors++
			case "warn":
				rep.Warnings++
			default:
				rep.Infos++
			}
		}
	}
	return rep
}
