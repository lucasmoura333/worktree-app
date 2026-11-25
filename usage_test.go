package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func usageAgent(agent, session, cwd string) AgentInfo {
	return AgentInfo{
		AgentRecord: AgentRecord{Agent: agent, Session: session, Cwd: cwd},
		Alive:       true,
	}
}

func TestUsageClaudeIncrementalAndDedupe(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/p/acme-app/dev.x"
	dir := filepath.Join(home, ".claude", "projects", "-p-acme-app-dev-x")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "sess1.jsonl")
	lines := `{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":1000,"cache_creation_input_tokens":50}}}
{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":999,"output_tokens":999,"cache_read_input_tokens":999,"cache_creation_input_tokens":999}}}
{"type":"user","message":{"content":"segredo que não pode sair"}}
{"type":"assistant","message":{"id":"m2","usage":{"input_tokens":200,"output_tokens":20,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}
`
	if err := os.WriteFile(file, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	ur := &usageReader{}
	agents := []AgentInfo{usageAgent("claude", "sess1", cwd)}
	ur.fill(context.Background(), agents)
	u := agents[0].Usage
	if u == nil || u.Source != "claude-code" || u.In != 300 || u.Out != 30 || u.CacheRead != 1000 || u.CacheWrite != 50 {
		t.Fatalf("primeira leitura: %+v", u)
	}

	// O streaming repete id: só a linha nova conta, e m1/m2 não voltam.
	more := `{"type":"assistant","message":{"id":"m3","usage":{"input_tokens":5,"output_tokens":1}}}`
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(more); err != nil {
		t.Fatal(err)
	}
	f.Close()
	agents = []AgentInfo{usageAgent("claude", "sess1", cwd)}
	ur.fill(context.Background(), agents)
	u = agents[0].Usage
	if u == nil || u.In != 305 || u.Out != 31 {
		t.Fatalf("leitura incremental: %+v", u)
	}
}

func TestUsageCodexCumulative(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	session := "01a0e9be-18d2-7ee3-838c-5eb5a2a34253"
	dir := filepath.Join(home, ".codex", "sessions", "2026", "11", "25")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "rollout-2026-11-25T10-00-00-"+session+".jsonl")
	lines := `{"type":"session_meta","payload":{"id":"` + session + `"}}
{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":800,"cache_write_input_tokens":10,"output_tokens":40,"reasoning_output_tokens":5}}}}
{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1500,"cached_input_tokens":1200,"cache_write_input_tokens":20,"output_tokens":90,"reasoning_output_tokens":12}}}}
`
	if err := os.WriteFile(file, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	ur := &usageReader{}
	agents := []AgentInfo{usageAgent("codex", session, "/p/acme-app/dev.x")}
	ur.fill(context.Background(), agents)
	u := agents[0].Usage
	if u == nil || u.Source != "codex" || u.In != 300 || u.Out != 90 || u.CacheRead != 1200 || u.CacheWrite != 20 || u.Reasoning != 12 {
		t.Fatalf("codex: %+v", u)
	}
}

func TestUsageOpencodeViaCLI(t *testing.T) {
	old := opencodeDB
	t.Cleanup(func() { opencodeDB = old })
	calls := 0
	opencodeDB = func(_ context.Context, bin, query string) ([]byte, error) {
		calls++
		if bin != "opencode" {
			t.Fatalf("bin = %q", bin)
		}
		if !strings.Contains(query, "session_id='ses_1'") || !strings.Contains(query, "json_extract(data,'$.tokens.input')") {
			t.Fatalf("consulta inesperada: %s", query)
		}
		return []byte(`[{"n":2,"tin":10,"tout":2,"reason":1,"cr":100,"cw":5,"cost":0.25}]`), nil
	}
	ur := &usageReader{ocBin: "opencode", ocChecked: true}
	agents := []AgentInfo{usageAgent("opencode", "ses_1", "/p/acme-app/dev.x")}
	ur.fill(context.Background(), agents)
	u := agents[0].Usage
	if u == nil || u.Source != "opencode" || u.In != 10 || u.Out != 2 || u.CacheRead != 100 || u.CacheWrite != 5 || u.Reasoning != 1 || !u.HasCost || u.CostUSD != 0.25 {
		t.Fatalf("opencode: %+v", u)
	}
	// Mesma sessão dentro do TTL não roda o CLI de novo.
	agents = []AgentInfo{usageAgent("opencode", "ses_1", "/p/acme-app/dev.x")}
	ur.fill(context.Background(), agents)
	if calls != 1 {
		t.Fatalf("chamou o CLI %d vezes; queria 1", calls)
	}
}

func TestUsageUnknownAgentStaysEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ur := &usageReader{}
	agents := []AgentInfo{usageAgent("agy", "conv1", "/p/acme-app/dev.x")}
	ur.fill(context.Background(), agents)
	if agents[0].Usage != nil {
		t.Fatalf("agy não tem fonte numérica: %+v", agents[0].Usage)
	}
}
