package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain isola o stateDir: nenhum teste grava em ~/.local/state real.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "wt-state-")
	if err != nil {
		panic(err)
	}
	os.Setenv("WT_STATE_DIR", dir)
	// Git for Windows vem com core.autocrlf=true; os testes comparam bytes.
	os.Setenv("GIT_CONFIG_COUNT", "1")
	os.Setenv("GIT_CONFIG_KEY_0", "core.autocrlf")
	os.Setenv("GIT_CONFIG_VALUE_0", "false")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// hookStdout roda o hook e devolve o que foi escrito no stdout.
func hookStdout(t *testing.T, agent, payload string, now time.Time) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	runHook([]string{"--agent", agent}, strings.NewReader(payload), now)
	os.Stdout = old
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

// hookOut roda o hook capturando o stdout, que precisa ficar vazio.
func hookOut(t *testing.T, agent, payload string, now time.Time) {
	t.Helper()
	if out := hookStdout(t, agent, payload, now); out != "" {
		t.Fatalf("hook escreveu no stdout: %q", out)
	}
}

func hookRec(t *testing.T, dir, agent, session string) (AgentRecord, bool) {
	t.Helper()
	return readAgentRecord(filepath.Join(dir, agent+"-"+session+".json"))
}

func TestHookStatesAndSilence(t *testing.T) {
	state := t.TempDir()
	t.Setenv("WT_STATE_DIR", state)
	dir := filepath.Join(state, "agents")
	cwd := t.TempDir()
	t0 := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	steps := []struct {
		agent, payload, want string
	}{
		{"claude", `{"session_id":"s1","cwd":` + jsonQuote(cwd) + `,"hook_event_name":"UserPromptSubmit","prompt":"segredo"}`, agentWorking},
		{"claude", `{"session_id":"s1","cwd":` + jsonQuote(cwd) + `,"hook_event_name":"Notification","notification_type":"permission_prompt"}`, agentWaiting},
		{"claude", `{"session_id":"s1","cwd":` + jsonQuote(cwd) + `,"hook_event_name":"PostToolUse"}`, agentWorking},
		{"claude", `{"session_id":"s1","cwd":` + jsonQuote(cwd) + `,"hook_event_name":"Stop"}`, agentDone},
		{"claude", `{"session_id":"s1","cwd":` + jsonQuote(cwd) + `,"hook_event_name":"SessionEnd"}`, agentEnded},
		{"codex", `{"session_id":"c-1","cwd":` + jsonQuote(cwd) + `,"hook_event_name":"PermissionRequest","turn_id":"x"}`, agentWaiting},
		{"opencode", `{"session_id":"ses_abc","cwd":` + jsonQuote(cwd) + `,"state":"working"}`, agentWorking},
	}
	for i, s := range steps {
		hookOut(t, s.agent, s.payload, t0.Add(time.Duration(i)*time.Second))
		var sess string
		_ = json.Unmarshal([]byte(s.payload), &struct {
			S *string `json:"session_id"`
		}{&sess})
		r, ok := hookRec(t, dir, s.agent, sess)
		if !ok || r.State != s.want || r.Cwd != cwd {
			t.Fatalf("passo %d: %+v ok=%v, quer %s", i, r, ok, s.want)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "claude-s1.json"))
	if strings.Contains(string(b), "segredo") {
		t.Fatal("prompt vazou para o registro")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(filepath.Join(dir, "claude-s1.json")); fi.Mode().Perm() != 0o600 {
			t.Fatalf("permissão %v", fi.Mode().Perm())
		}
		if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
			t.Fatalf("permissão da pasta %v", fi.Mode().Perm())
		}
	}

	// Mesmo estado não regrava e mantém o "since".
	hookOut(t, "codex", `{"session_id":"c-1","cwd":`+jsonQuote(cwd)+`,"hook_event_name":"PermissionRequest"}`, t0.Add(time.Hour))
	if r, _ := hookRec(t, dir, "codex", "c-1"); !r.Since.Equal(t0.Add(5 * time.Second)) {
		t.Fatalf("since mudou: %v", r.Since)
	}

	// Entradas recusadas não criam arquivo.
	before, _ := os.ReadDir(dir)
	for _, bad := range []struct{ agent, payload string }{
		{"claude", `{"session_id":"../../etc/x","cwd":"/tmp","hook_event_name":"Stop"}`},
		{"claude", `{"session_id":"ok","cwd":"relativo","hook_event_name":"Stop"}`},
		{"claude", `{"session_id":"ok2","cwd":"/tmp","hook_event_name":"SubagentStop"}`},
		{"cursor", `{"session_id":"ok3","cwd":"/tmp","hook_event_name":"Stop"}`},
		{"claude", `não é json`},
	} {
		hookOut(t, bad.agent, bad.payload, t0)
	}
	if after, _ := os.ReadDir(dir); len(after) != len(before) {
		t.Fatalf("entrada inválida gravou arquivo: %d -> %d", len(before), len(after))
	}
}

// O agy usa camelCase e exige uma resposta JSON no stdout; o resto dos agentes
// continua mudo. A sessão dele vem em conversationId.
func TestAgyHookStateAndReply(t *testing.T) {
	if !knownAgents["agy"] {
		t.Fatal("agy não está em knownAgents")
	}
	state := t.TempDir()
	t.Setenv("WT_STATE_DIR", state)
	dir := filepath.Join(state, "agents")
	cwd := t.TempDir()
	t0 := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	steps := []struct {
		payload, wantState, wantOut string
	}{
		{`{"conversationId":"agy-1","cwd":` + jsonQuote(cwd) + `,"hookEventName":"SessionStart","lastUserInput":"segredo"}`, agentWaiting, `{}`},
		{`{"conversationId":"agy-1","cwd":` + jsonQuote(cwd) + `,"hookEventName":"PreInvocation"}`, agentWorking, `{}`},
		{`{"conversationId":"agy-1","cwd":` + jsonQuote(cwd) + `,"hookEventName":"PostToolUse"}`, agentWorking, `{}`},
		{`{"conversationId":"agy-1","workspacePaths":[` + jsonQuote(cwd) + `],"hookEventName":"PreInvocation"}`, agentWorking, `{}`},
		{`{"conversationId":"agy-1","cwd":` + jsonQuote(cwd) + `,"hookEventName":"Stop"}`, agentDone, `{"decision":"allow"}`},
	}
	for i, s := range steps {
		out := hookStdout(t, "agy", s.payload, t0.Add(time.Duration(i)*time.Second))
		if out != s.wantOut {
			t.Fatalf("passo %d: stdout %q, quer %q", i, out, s.wantOut)
		}
		r, ok := hookRec(t, dir, "agy", "agy-1")
		if !ok || r.State != s.wantState || r.Cwd != cwd {
			t.Fatalf("passo %d: %+v ok=%v, quer %s em %s", i, r, ok, s.wantState, cwd)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "agy-agy-1.json"))
	if strings.Contains(string(b), "segredo") {
		t.Fatal("prompt vazou para o registro do agy")
	}
	// Mesmo inválido, o hook do agy responde JSON (não deixa o CLI sem resposta).
	if out := hookStdout(t, "agy", `{"conversationId":"../../x","cwd":`+jsonQuote(cwd)+`,"hookEventName":"Stop"}`, t0); out != `{"decision":"allow"}` {
		t.Fatalf("stdout para sessão inválida: %q", out)
	}
}

func TestMatchWorktreeBoundary(t *testing.T) {
	entries := []wtEntry{
		{Repo: "app", Path: "/p/gdpro-app", Main: true},
		{Repo: "app", Path: "/p/gdpro-app-x", Feature: "x"},
	}
	cases := map[string]string{
		"/p/gdpro-app-x/resources/js": "/p/gdpro-app-x",
		"/p/gdpro-app/src":            "/p/gdpro-app",
		"/p/gdpro-app":                "/p/gdpro-app",
		"/p/gdpro-apps":               "",
		"/home":                       "",
	}
	for cwd, want := range cases {
		e, ok := matchWorktree(entries, cwd)
		if got := map[bool]string{true: e.Path}[ok]; got != want {
			t.Errorf("%s: casou %q, quer %q", cwd, got, want)
		}
	}
}

// fakeProc cria /proc/<pid>/{comm,stat,cwd} num diretório temporário.
func fakeProc(t *testing.T, root string, pid, ppid int, comm, cwd string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("varredura de /proc só existe no Linux")
	}
	d := filepath.Join(root, strconv.Itoa(pid))
	write(t, filepath.Join(d, "comm"), comm+"\n")
	write(t, filepath.Join(d, "stat"), strconv.Itoa(pid)+" ("+comm+") S "+strconv.Itoa(ppid)+" 1 1\n")
	write(t, filepath.Join(d, "cmdline"), comm+"\x00")
	if err := os.Symlink(cwd, filepath.Join(d, "cwd")); err != nil {
		t.Fatal(err)
	}
}

func TestCollectAgentsLivenessAndCounts(t *testing.T) {
	proc := t.TempDir()
	old := procRoot
	procRoot = proc
	t.Cleanup(func() { procRoot = old })

	wtA, wtB := "/p/acme-app-a", "/p/acme-app-b"
	entries := []wtEntry{{Repo: "app", Path: wtA, Feature: "a"}, {Repo: "app", Path: wtB, Feature: "b"}, {Repo: "app", Path: "/p/acme-app", Main: true}}
	fakeProc(t, proc, 100, 1, "claude", wtA)
	fakeProc(t, proc, 101, 100, "claude", wtA) // filho do mesmo agente: ignorado
	fakeProc(t, proc, 200, 1, "codex", wtB+"/sub")
	fakeProc(t, proc, 300, 1, "opencode", "/p/acme-app")
	fakeProc(t, proc, 400, 1, "bash", wtA)

	now := time.Now()
	recs := []AgentRecord{
		{Agent: "claude", Session: "vivo", Cwd: wtA, State: agentWaiting, TS: now, PID: 100},
		{Agent: "claude", Session: "antigo", Cwd: wtA, State: agentDone, TS: now.Add(-time.Hour), PID: 100},
		{Agent: "claude", Session: "morto", Cwd: wtB, State: agentWorking, TS: now, PID: 999},
		{Agent: "codex", Session: "sem-pid", Cwd: wtB, State: agentWorking, TS: now},
		{Agent: "claude", Session: "fora", Cwd: "/home/x", State: agentWorking, TS: now, PID: 100},
	}
	st := collectAgents(entries, recs, scanProcAgents(), "dir")
	by := map[string]AgentInfo{}
	for _, a := range st.Agents {
		by[a.Agent+"/"+a.Session] = a
	}
	if _, ok := by["claude/fora"]; ok {
		t.Fatal("sessão fora das worktrees apareceu")
	}
	if a := by["claude/vivo"]; !a.Alive || a.State != agentWaiting || a.Feature != "a" {
		t.Fatalf("vivo: %+v", a)
	}
	if a := by["claude/antigo"]; a.Alive || a.State != agentEnded {
		t.Fatalf("sessão antiga no mesmo pid ficou viva: %+v", a)
	}
	if a := by["claude/morto"]; a.Alive || a.State != agentEnded {
		t.Fatalf("morto: %+v", a)
	}
	if a := by["codex/sem-pid"]; !a.Alive || a.PID != 200 {
		t.Fatalf("codex sem pid não casou com o processo: %+v", a)
	}
	if a := by["opencode/"]; !a.NoHook || !a.Main || a.State != "running" {
		t.Fatalf("opencode sem hook: %+v", a)
	}
	if st.Counts != (AgentCounts{Working: 1, Waiting: 1, Running: 1}) {
		t.Fatalf("contagens: %+v", st.Counts)
	}
	if len(st.Agents) != 5 {
		t.Fatalf("esperava 5 entradas, veio %d: %+v", len(st.Agents), st.Agents)
	}
}

// Duas sessões vivas na mesma worktree precisam aparecer em liveByPath para o
// card avisar antes de abrir uma terceira.
func TestCollectAgentsConcurrentPerWorktree(t *testing.T) {
	proc := t.TempDir()
	old := procRoot
	procRoot = proc
	t.Cleanup(func() { procRoot = old })

	wt := "/p/acme-app-a"
	entries := []wtEntry{{Repo: "app", Path: wt, Feature: "a"}}
	fakeProc(t, proc, 100, 1, "claude", wt)
	fakeProc(t, proc, 200, 1, "agy", wt+"/src")
	fakeProc(t, proc, 300, 1, "codex", "/p/acme-app-b")

	st := collectAgents(entries, nil, scanProcAgents(), "dir")
	if got := st.LiveByPath[wt]; got != 2 {
		t.Fatalf("liveByPath[%s] = %d, quer 2: %+v", wt, got, st.LiveByPath)
	}
	if got := st.LiveByPath["/p/acme-app-b"]; got != 0 {
		t.Fatalf("worktree fora da lista entrou: %+v", st.LiveByPath)
	}
	if len(st.Agents) != 2 || !st.Agents[0].NoHook || !st.Agents[1].NoHook {
		t.Fatalf("agentes: %+v", st.Agents)
	}
}

func TestAgentsEndpointUsesRealWorktrees(t *testing.T) {
	cfg := fixture(t)
	cfg.StateDir = t.TempDir()
	ctx := context.Background()
	if err := createFeature(ctx, cfg, &CreateReq{Feature: "f1", Repos: []CreateRepoReq{{Key: "app"}}}, newPortAllocator(), &memLog{}); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(cfg.Root, "acme-app", "dev.f1")
	rec := AgentRecord{Agent: "codex", Session: "x1", Cwd: filepath.Join(wt, "src"), State: agentDone, TS: time.Now()}
	write(t, filepath.Join(cfg.StateDir, "agents", "codex-x1.json"), string(mustJSON(rec)))
	write(t, filepath.Join(cfg.StateDir, "agents", "lixo.json"), "{")

	s := newServer(cfg)
	req := httptest.NewRequest("GET", "/api/agents", nil)
	req.Host = "127.0.0.1:7777"
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var st AgentsState
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Agents) != 1 || st.Agents[0].Feature != "f1" || st.Agents[0].Repo != "app" || st.Agents[0].Alive {
		t.Fatalf("agentes: %+v", st.Agents)
	}
	if exists(filepath.Join(wt, ".git", "index.lock")) {
		t.Fatal("criou index.lock")
	}
}
