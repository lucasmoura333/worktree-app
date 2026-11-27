package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func diagItems(rep DiagReport, id string) []DiagItem {
	for _, c := range rep.Checks {
		if c.ID == id {
			return c.Items
		}
	}
	return nil
}

func diagHas(items []DiagItem, severity, titleContains string) bool {
	for _, it := range items {
		if it.Severity == severity && strings.Contains(it.Title, titleContains) {
			return true
		}
	}
	return false
}

func gitc(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)
	run(t, dir, "git", full...)
}

func TestDiagnosticoEndpoint(t *testing.T) {
	cfg := fixture(t)
	s := newServer(cfg)

	w := apiCall(t, s, http.MethodGet, "/api/diagnostico?fresh=1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/diagnostico: %d %s", w.Code, w.Body.String())
	}
	var rep DiagReport
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Checks) != 6 {
		t.Fatalf("checks = %d, queria 6", len(rep.Checks))
	}
	if rep.At.IsZero() {
		t.Fatal("relatório sem data")
	}
	// Repo recém-clonado e limpo: só a main existe e não há nada a apontar.
	if rep.Errors != 0 || rep.Warnings != 0 {
		t.Fatalf("fixture limpa já tem problemas: %+v", rep)
	}
}

func TestDiagnosticoEncontraProblemas(t *testing.T) {
	cfg := fixture(t)
	ctx := context.Background()
	lg := &memLog{}
	for _, feat := range []string{"feat1", "feat2"} {
		req := &CreateReq{Feature: feat, Repos: []CreateRepoReq{{Key: "app"}}}
		if err := createFeature(ctx, cfg, req, newPortAllocator(), lg); err != nil {
			t.Fatalf("create %s: %v\n%s", feat, err, lg)
		}
	}
	// feat2 passa a usar a mesma porta do docker que feat1.
	port, _ := getEnvValue(read(t, filepath.Join(cfg.Root, "acme-app", "dev.feat1", "docker/dev/.env")), "DEV_PORT")
	env2 := filepath.Join(cfg.Root, "acme-app", "dev.feat2", "docker/dev/.env")
	write(t, env2, "COMPOSE_PROJECT_NAME=acme-f2\nDEV_PORT="+port+"\n")

	main := cfg.mainPath(&cfg.Repos[0])
	// Branch já contida na base e sem worktree: pode apagar.
	run(t, main, "git", "branch", "ja-mergeada")
	// Branch com commit local e sem upstream: precisa publicar ou apagar.
	gitc(t, main, "checkout", "-q", "-b", "solta")
	write(t, filepath.Join(main, "solto.txt"), "x\n")
	gitc(t, main, "add", "solto.txt")
	gitc(t, main, "commit", "-q", "-m", "local")
	gitc(t, main, "checkout", "-q", "dev")

	// Histórico com uma falha.
	if err := appendOp(cfg, opRecord{ID: "x", Op: "create", Start: time.Now(), OK: false, Error: "falha fake"}); err != nil {
		t.Fatal(err)
	}
	// Sessão de agente apontando para fora das worktrees.
	dir := filepath.Join(cfg.StateDir, "agents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec, _ := json.Marshal(AgentRecord{Agent: "claude", Session: "sess-1", Cwd: filepath.Join(cfg.Root, "outra-pasta"), State: agentDone, TS: time.Now()})
	write(t, filepath.Join(dir, "sess-1.json"), string(rec))

	rep := buildDiagnostics(ctx, newServer(cfg), false)

	if got := diagItems(rep, "ports"); !diagHas(got, "error", "duplicada") {
		t.Errorf("porta duplicada não apareceu: %+v", got)
	}
	branches := diagItems(rep, "branches")
	if !diagHas(branches, "info", "mergeada") {
		t.Errorf("branch mergeada não apareceu: %+v", branches)
	}
	if !diagHas(branches, "warn", "commits só no local") {
		t.Errorf("branch sem upstream não apareceu: %+v", branches)
	}
	if got := diagItems(rep, "history"); !diagHas(got, "warn", "create falhou") {
		t.Errorf("falha do histórico não apareceu: %+v", got)
	}
	if got := diagItems(rep, "sessions"); !diagHas(got, "info", "fora das worktrees") {
		t.Errorf("sessão órfã não apareceu: %+v", got)
	}
	if rep.Errors == 0 || rep.Warnings == 0 || rep.Infos == 0 {
		t.Errorf("contagem não reflete os itens: %+v", rep)
	}
}

func TestDiagnosticoSnapshotCorrompido(t *testing.T) {
	cfg := fixture(t)
	ctx := context.Background()
	lg := &memLog{}
	req := &CreateReq{Feature: "feat-snap", Repos: []CreateRepoReq{{Key: "app"}}}
	if err := createFeature(ctx, cfg, req, newPortAllocator(), lg); err != nil {
		t.Fatalf("create: %v\n%s", err, lg)
	}
	st := scanState(ctx, cfg)
	f := findFeature(st, "feat-snap")
	if f == nil || len(f.Worktrees) != 1 {
		t.Fatalf("worktree não encontrada: %+v", f)
	}
	wt := f.Worktrees[0]
	write(t, filepath.Join(wt.Path, "yarn.lock"), "lock-dirty\n")
	meta, err := createSnapshot(ctx, cfg, &cfg.Repos[0], wt, "manual", "", lg)
	if err != nil {
		t.Fatalf("snapshot: %v\n%s", err, lg)
	}
	if !meta.HasPatch {
		t.Fatalf("snapshot sem patch: %+v", meta)
	}
	// Depois de selado, um byte a mais invalida o SHA256.
	pf, err := os.OpenFile(filepath.Join(meta.Dir, "changes.patch"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = pf.WriteString("corrompido\n")
	_ = pf.Close()

	rep := buildDiagnostics(ctx, newServer(cfg), false)
	items := diagItems(rep, "snapshots")
	if !diagHas(items, "error", "snapshot inválido") {
		t.Fatalf("corrupção não detectada: %+v", items)
	}
}

func TestDiagnosticoBusyAdiaSnapshots(t *testing.T) {
	cfg := fixture(t)
	s := newServer(cfg)
	s.opMu.Lock()
	defer s.opMu.Unlock()
	rep := buildDiagnostics(context.Background(), s, true)
	if !rep.Busy {
		t.Fatal("relatório deveria marcar busy")
	}
	got := diagItems(rep, "snapshots")
	if len(got) != 1 || got[0].Title != "verificação adiada" {
		t.Fatalf("snapshots deveriam ficar adiados: %+v", got)
	}
}

func TestDemoDiagnostico(t *testing.T) {
	rep := demoDiagnostics()
	if len(rep.Checks) != 6 {
		t.Fatalf("checks = %d", len(rep.Checks))
	}
	if rep.Errors == 0 || rep.Warnings == 0 || rep.Infos == 0 {
		t.Fatalf("demo precisa mostrar os três níveis: %+v", rep)
	}
}
