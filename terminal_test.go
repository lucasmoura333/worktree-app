package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTerminalArgvAndValidation(t *testing.T) {
	tmpl := []string{"wt.exe", "-w", "0", "nt", "--title", "{title}", "wsl.exe", "-d", "{distro}", "--cd", "{path}", "-e", "{cmd}"}
	vars := map[string]string{"{title}": "claude-f1", "{path}": "/p/app-f1", "{distro}": "Ubuntu-24.04"}
	got, err := terminalArgv(tmpl, vars, []string{"/bin/wt", "run", "claude", "--resume", "abc-1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"wt.exe", "-w", "0", "nt", "--title", "claude-f1", "wsl.exe", "-d", "Ubuntu-24.04", "--cd", "/p/app-f1", "-e", "/bin/wt", "run", "claude", "--resume", "abc-1"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv:\n%v\n%v", got, want)
	}
	for _, bad := range []string{"a;b", "a b", "a&b", `a"b`, "$(x)", "a|b"} {
		if _, err := terminalArgv(tmpl, vars, []string{"x", bad}); err == nil {
			t.Errorf("aceitou %q", bad)
		}
	}
	if _, err := terminalArgv(tmpl, map[string]string{"{title}": "t", "{path}": "/p/com espaço", "{distro}": "d"}, nil); err == nil {
		t.Error("aceitou caminho com espaço")
	}
	if safeTitle("feat; rm -rf /") != "feat-rm--rf" {
		t.Errorf("safeTitle: %q", safeTitle("feat; rm -rf /"))
	}

	c := &Config{Terminal: tmpl, Agents: map[string]AgentLaunch{"x": {}}}
	if c.validateTerminal() == nil {
		t.Error("aceitou agente sem new")
	}
	c.Agents = map[string]AgentLaunch{"x": {New: []string{"x", "--flag;rm"}}}
	if c.validateTerminal() == nil {
		t.Error("aceitou argumento com ;")
	}
	c.Agents = defaultAgents()
	if err := c.validateTerminal(); err != nil {
		t.Errorf("padrão recusado: %v", err)
	}
	c.Terminal = []string{"wt.exe", "{path}"}
	if c.validateTerminal() == nil {
		t.Error("aceitou terminal sem {cmd}")
	}
}

func postTerminal(s *server, body string, xwt bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/terminal", strings.NewReader(body))
	req.Host = "127.0.0.1:7777"
	if xwt {
		req.Header.Set("X-WT", "1")
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, req)
	return w
}

func TestTerminalEndpoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("o terminal abre aba do WSL; caminho do Windows não passa no argRe")
	}
	cfg := fixture(t)
	cfg.StateDir = t.TempDir()
	cfg.Distro = "Ubuntu-24.04"
	cfg.Terminal = []string{"wt.exe", "--title", "{title}", "wsl.exe", "-d", "{distro}", "--cd", "{path}", "-e", "{cmd}"}
	cfg.Agents = defaultAgents()
	if err := createFeature(context.Background(), cfg, &CreateReq{Feature: "f1", Repos: []CreateRepoReq{{Key: "app"}}}, newPortAllocator(), &memLog{}); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(cfg.Root, "acme-app", "dev.f1")
	s := newServer(cfg)
	var launched [][]string
	s.launch = func(argv []string) error { launched = append(launched, argv); return nil }

	if w := postTerminal(s, `{"path":`+jsonQuote(wt)+`}`, false); w.Code != http.StatusForbidden {
		t.Fatalf("sem X-WT: %d", w.Code)
	}
	if w := postTerminal(s, `{"path":`+jsonQuote(cfg.Root)+`}`, true); w.Code != http.StatusBadRequest {
		t.Fatalf("root não é worktree: %d", w.Code)
	}
	if w := postTerminal(s, `{"path":"`+filepath.Join(wt, "sub")+`"}`, true); w.Code != http.StatusBadRequest {
		t.Fatalf("subpasta aceita: %d", w.Code)
	}
	if w := postTerminal(s, `{"path":`+jsonQuote(wt)+`,"agent":"vim"}`, true); w.Code != http.StatusBadRequest {
		t.Fatalf("agente desconhecido: %d", w.Code)
	}
	if w := postTerminal(s, `{"path":`+jsonQuote(wt)+`,"agent":"claude","resume":true}`, true); w.Code != http.StatusBadRequest {
		t.Fatalf("retomar sem sessão: %d", w.Code)
	}
	if len(launched) != 0 {
		t.Fatalf("lançou em caso recusado: %v", launched)
	}
	if r := getResumable(t, s); len(r) != 0 {
		t.Fatalf("retomável sem sessão registrada: %v", r)
	}

	if w := postTerminal(s, `{"path":`+jsonQuote(wt)+`}`, true); w.Code != http.StatusOK {
		t.Fatalf("terminal: %d %s", w.Code, w.Body)
	}
	got := launched[0]
	if got[2] != "f1" || got[5] != "Ubuntu-24.04" || got[7] != wt || got[len(got)-1] != "run" {
		t.Fatalf("argv do terminal: %v", got)
	}

	old := AgentRecord{Agent: "claude", Session: "velha", Cwd: wt, State: agentEnded, TS: time.Now().Add(-time.Hour)}
	nova := AgentRecord{Agent: "claude", Session: "nova-1", Cwd: filepath.Join(wt, "src"), State: agentDone, TS: time.Now()}
	write(t, filepath.Join(cfg.StateDir, "agents", "claude-velha.json"), string(mustJSON(old)))
	write(t, filepath.Join(cfg.StateDir, "agents", "claude-nova-1.json"), string(mustJSON(nova)))
	if r := getResumable(t, s); !slices.Equal(r[wt], []string{"claude"}) || len(r) != 1 {
		t.Fatalf("retomável com sessão registrada: %v", r)
	}
	if w := postTerminal(s, `{"path":`+jsonQuote(wt)+`,"agent":"claude","resume":true}`, true); w.Code != http.StatusOK {
		t.Fatalf("retomar: %d %s", w.Code, w.Body)
	}
	got = launched[1]
	if !slices.Equal(got[len(got)-4:], []string{"run", "claude", "--resume", "nova-1"}) || got[2] != "claude-f1" {
		t.Fatalf("argv do retomar: %v", got)
	}
}

// getResumable lê o campo "resumable" de /api/agents, o mesmo poll que
// atualiza os cards.
func getResumable(t *testing.T, s *server) map[string][]string {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/agents", nil)
	req.Host = "127.0.0.1:7777"
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/api/agents: %d %s", w.Code, w.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["resumable"]) == "null" || raw["resumable"] == nil {
		t.Fatalf("resumable ausente ou null: %s", w.Body)
	}
	var st AgentsState
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	return st.Resumable
}

func TestResumableAgents(t *testing.T) {
	wtA, wtB, wtC := "/p/acme-app-a", "/p/acme-app-b", "/p/acme-app-c"
	entries := []wtEntry{{Repo: "app", Path: wtA, Feature: "a"}, {Repo: "app", Path: wtB, Feature: "b"}, {Repo: "app", Path: wtC, Feature: "c"}}
	agents := map[string]AgentLaunch{
		"claude": {New: []string{"claude"}, Resume: []string{"claude", "--resume", "{session}"}},
		"codex":  {New: []string{"codex"}, Resume: []string{"codex", "resume", "{session}"}},
		"aider":  {New: []string{"aider"}}, // sem "resume": nunca retomável
	}
	now := time.Now()

	if got := resumableAgents(entries, nil, agents); got == nil || len(got) != 0 {
		t.Fatalf("sem registros: %v", got)
	}

	recs := []AgentRecord{
		{Agent: "claude", Session: "s1", Cwd: wtA + "/src", TS: now.Add(-time.Hour)},
		{Agent: "codex", Session: "c1", Cwd: wtA, TS: now},
		{Agent: "aider", Session: "x", Cwd: wtA, TS: now},
		{Agent: "vim", Session: "x", Cwd: wtA, TS: now},             // agente não configurado
		{Agent: "claude", Session: "fora", Cwd: "/home/x", TS: now}, // fora das worktrees
		// Em B o registro mais recente não tem sessão: lastSession devolve "".
		{Agent: "claude", Session: "velha", Cwd: wtB, TS: now.Add(-time.Hour)},
		{Agent: "claude", Session: "", Cwd: wtB, TS: now},
	}
	got := resumableAgents(entries, recs, agents)
	want := map[string][]string{wtA: {"claude", "codex"}}
	if len(got) != len(want) || !slices.Equal(got[wtA], want[wtA]) {
		t.Fatalf("retomáveis: %v, esperava %v", got, want)
	}

	// Mesmo critério do handler: retomável sse lastSession acha sessão.
	for _, e := range entries {
		for name, a := range agents {
			has := lastSession(entries, recs, name, e.Path) != "" && len(a.Resume) > 0
			if slices.Contains(got[e.Path], name) != has {
				t.Errorf("%s em %s: resumable=%v, lastSession=%v", name, e.Path, !has, has)
			}
		}
	}
}
