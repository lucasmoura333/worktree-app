package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type memLog struct {
	mu    sync.Mutex
	lines []string
}

func (m *memLog) Log(repo, level, msg string) {
	m.mu.Lock()
	m.lines = append(m.lines, level+" ["+repo+"] "+msg)
	m.mu.Unlock()
}

func (m *memLog) String() string { return strings.Join(m.lines, "\n") }

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fixture monta root/acme-app/dev clonado de um origin bare, com a branch
// "dev" e uma branch "outra" cujo yarn.lock difere. Todo .env é falso.
func fixture(t *testing.T) *Config {
	t.Helper()
	tmp := t.TempDir()
	origin := filepath.Join(tmp, "origin.git")
	seed := filepath.Join(tmp, "seed")
	run(t, tmp, "git", "init", "-q", "--bare", "-b", "dev", origin)
	run(t, tmp, "git", "init", "-q", "-b", "dev", seed)
	write(t, filepath.Join(seed, ".gitignore"), "node_modules/\n.env\ndocker/dev/.env\n.claude/\n")
	write(t, filepath.Join(seed, "yarn.lock"), "lock-v1\n")
	write(t, filepath.Join(seed, "docker/dev/.env.example"), "COMPOSE_PROJECT_NAME=\nDEV_PORT=\n")
	gitc := []string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", append(gitc, "commit", "-q", "-m", "init")...)
	run(t, seed, "git", "checkout", "-q", "-b", "outra")
	write(t, filepath.Join(seed, "yarn.lock"), "lock-v2\n")
	run(t, seed, "git", append(gitc, "commit", "-q", "-am", "lock novo")...)
	run(t, seed, "git", "remote", "add", "origin", origin)
	run(t, seed, "git", "push", "-q", "origin", "dev", "outra")

	root := filepath.Join(tmp, "projects")
	main := filepath.Join(root, "acme-app", "dev")
	if err := os.MkdirAll(filepath.Dir(main), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, tmp, "git", "clone", "-q", "-b", "dev", origin, main)
	write(t, filepath.Join(main, ".env"), "FAKE_SECRET=nao-e-real\n")
	write(t, filepath.Join(main, "docker/dev/.env"), "COMPOSE_PROJECT_NAME=acme-teste\nDEV_PORT=46660\n")
	write(t, filepath.Join(main, ".claude/settings.local.json"), "{}\n")
	write(t, filepath.Join(main, "node_modules/pkg/index.js"), "module.exports = 1\n")
	write(t, filepath.Join(root, "dev.code-workspace"), `{
  "folders": [{ "path": "./acme-app/dev" },],
  // comentário
  "settings": { "editor.tabSize": 2, },
}`)

	cfg := &Config{
		Root:              root,
		WorkspaceTemplate: "dev.code-workspace",
		Repos: []RepoConfig{{
			Key: "app", Dir: "acme-app", Main: "dev", Base: "origin/dev",
			Copy:      []string{".env", "docker/dev/.env", ".claude", "nao-existe.txt"},
			Deps:      []DepConfig{{Dir: "node_modules", Install: []string{"git", "init", "-q", "node_modules"}}},
			DockerEnv: &DockerEnvConfig{File: "docker/dev/.env", ProjectKey: "COMPOSE_PROJECT_NAME", PortKey: "DEV_PORT", PortMin: 46660, PortMax: 46699},
		}},
	}
	applyDefaults(cfg)
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func findFeature(st *State, name string) *FeatureState {
	for i := range st.Features {
		if st.Features[i].Name == name {
			return &st.Features[i]
		}
	}
	return nil
}

func TestCreateCopyAndRemove(t *testing.T) {
	cfg := fixture(t)
	ctx := context.Background()
	ports := newPortAllocator()
	lg := &memLog{}
	req := &CreateReq{Feature: "feat1", Repos: []CreateRepoReq{{Key: "app"}}, Fetch: true, Tasks: true, Workspace: true}
	if err := createFeature(ctx, cfg, req, ports, lg); err != nil {
		t.Fatalf("create: %v\n%s", err, lg)
	}
	wt := filepath.Join(cfg.Root, "acme-app", "dev.feat1")
	if got := read(t, filepath.Join(wt, ".env")); got != "FAKE_SECRET=nao-e-real\n" {
		t.Fatalf(".env não copiado igual: %q", got)
	}
	if !exists(filepath.Join(wt, ".claude/settings.local.json")) {
		t.Fatal(".claude não copiado")
	}
	denv := read(t, filepath.Join(wt, "docker/dev/.env"))
	if v, _ := getEnvValue(denv, "COMPOSE_PROJECT_NAME"); v != "acme-teste-feat1" {
		t.Fatalf("project = %q", v)
	}
	if v, _ := getEnvValue(denv, "DEV_PORT"); v == "46660" || v == "" {
		t.Fatalf("porta não trocada: %q", v)
	}
	if exists(filepath.Join(wt, "node_modules")) {
		t.Fatal("node_modules não pode ser copiado nem instalado sem pedido")
	}
	if strings.Contains(lg.String(), "nao-e-real") {
		t.Fatal("conteúdo do .env vazou para o log")
	}
	wantBranch := "dev.feat1." + time.Now().Format("020106")
	out, _ := git(ctx, wt, "rev-parse", "--abbrev-ref", "HEAD")
	if strings.TrimSpace(out) != wantBranch {
		t.Fatalf("branch = %q, quer %q", out, wantBranch)
	}
	if _, err := git(ctx, wt, "rev-parse", "--abbrev-ref", "@{u}"); err == nil {
		t.Fatal("branch nova não deveria ter upstream")
	}
	if !exists(filepath.Join(cfg.Root, "tasks/feat1/README.md")) {
		t.Fatal("tasks não criada")
	}
	paths, err := workspaceFolderPaths(cfg.Root, filepath.Join(cfg.Root, "feat1.code-workspace"))
	if err != nil || len(paths) != 2 || !samePath(paths[0], wt) {
		t.Fatalf("workspace: %v %v", paths, err)
	}
	var ws map[string]any
	_ = json.Unmarshal([]byte(read(t, filepath.Join(cfg.Root, "feat1.code-workspace"))), &ws)
	if ws["settings"] == nil {
		t.Fatal("settings do template não copiados")
	}

	// O plano seguinte já precisa enxergar a pasta e a branch como ocupadas.
	p := buildPlan(ctx, cfg, req, ports, false)
	if p.OK {
		t.Fatal("plano repetido deveria ter problemas")
	}

	st := scanState(ctx, cfg)
	f := findFeature(st, "feat1")
	if f == nil || len(f.Worktrees) != 1 || len(f.Workspaces) != 1 || f.TasksDir == "" {
		t.Fatalf("scan: %+v", f)
	}
	if wtI := f.Worktrees[0]; len(wtI.MissingFiles) != 0 || len(wtI.MissingDeps) != 1 || wtI.Dirty != 0 || wtI.Unpushed != 0 {
		t.Fatalf("status inesperado: %+v", wtI)
	}

	// Alteração não commitada bloqueia; forçar remove.
	write(t, filepath.Join(wt, "novo.txt"), "x")
	lg = &memLog{}
	if err := removeFeature(ctx, cfg, &RemoveReq{Feature: "feat1", Workspace: true}, lg); err == nil {
		t.Fatal("remoção com alteração deveria ser bloqueada")
	}
	if !isDir(wt) {
		t.Fatal("pasta removida apesar do bloqueio")
	}
	if err := removeFeature(ctx, cfg, &RemoveReq{Feature: "feat1", Force: true, Workspace: true}, lg); err != nil {
		t.Fatalf("remove forçado: %v\n%s", err, lg)
	}
	if exists(wt) || exists(filepath.Join(cfg.Root, "feat1.code-workspace")) {
		t.Fatal("worktree ou workspace sobraram")
	}
	if !exists(filepath.Join(cfg.Root, "tasks/feat1/README.md")) {
		t.Fatal("tasks não pode ser apagada")
	}
}

// Worktrees montadas à mão podem ter node_modules como junction para o main
// (como acme-api/dev.seguranca-tempo-real); remover não pode apagar o original.
func TestRemoveManualJunctionKeepsMain(t *testing.T) {
	cfg := fixture(t)
	ctx := context.Background()
	lg := &memLog{}
	req := &CreateReq{Feature: "feat2", Repos: []CreateRepoReq{{Key: "app", Mode: "new"}}}
	if err := createFeature(ctx, cfg, req, newPortAllocator(), lg); err != nil {
		t.Fatalf("create: %v\n%s", err, lg)
	}
	wt := filepath.Join(cfg.Root, "acme-app", "dev.feat2")
	link, target := filepath.Join(wt, "node_modules"), filepath.Join(cfg.mainPath(&cfg.Repos[0]), "node_modules")
	if runtime.GOOS == "windows" {
		run(t, wt, "cmd", "/c", "mklink", "/J", link, target)
	} else if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	f := findFeature(scanState(ctx, cfg), "feat2")
	if f == nil || len(f.Worktrees[0].LinkedDeps) != 1 {
		t.Fatalf("scan não viu o link: %+v", f)
	}
	if err := removeFeature(ctx, cfg, &RemoveReq{Feature: "feat2", DeleteBranch: true}, lg); err != nil {
		t.Fatalf("remove: %v\n%s", err, lg)
	}
	if !exists(filepath.Join(cfg.mainPath(&cfg.Repos[0]), "node_modules/pkg/index.js")) {
		t.Fatal("remover a worktree apagou o node_modules do main")
	}
	if refExists(ctx, cfg.mainPath(&cfg.Repos[0]), "refs/heads/dev.feat2."+time.Now().Format("020106")) {
		t.Fatal("branch deveria ter sido apagada")
	}
}

func TestExistingRemoteBranchInstalls(t *testing.T) {
	cfg := fixture(t)
	ctx := context.Background()
	lg := &memLog{}
	req := &CreateReq{Feature: "rev", Repos: []CreateRepoReq{{Key: "app", Mode: "existing", Branch: "origin/outra"}}, Install: true}
	if err := createFeature(ctx, cfg, req, newPortAllocator(), lg); err != nil {
		t.Fatalf("create: %v\n%s", err, lg)
	}
	wt := filepath.Join(cfg.Root, "acme-app", "dev.rev")
	out, _ := git(ctx, wt, "rev-parse", "--abbrev-ref", "@{u}")
	if strings.TrimSpace(out) != "origin/outra" {
		t.Fatalf("upstream = %q", out)
	}
	// yarn.lock difere do main, então tem que instalar em vez de copiar.
	if exists(filepath.Join(wt, "node_modules/pkg")) || !isDir(filepath.Join(wt, "node_modules/.git")) {
		t.Fatalf("esperava instalação, não cópia\n%s", lg)
	}
	// A mesma branch não pode abrir duas vezes.
	p := buildPlan(ctx, cfg, &CreateReq{Feature: "rev2", Repos: []CreateRepoReq{{Key: "app", Mode: "existing", Branch: "outra"}}}, newPortAllocator(), false)
	if p.OK {
		t.Fatal("branch já aberta deveria bloquear o plano")
	}
}

func TestSetupCompletesAndStale(t *testing.T) {
	cfg := fixture(t)
	ctx := context.Background()
	main := cfg.mainPath(&cfg.Repos[0])
	wt := filepath.Join(cfg.Root, "acme-app", "dev.manual")
	run(t, main, "git", "worktree", "add", "-q", "-b", "manual", wt, "origin/dev")
	f := findFeature(scanState(ctx, cfg), "manual")
	if f == nil || len(f.Worktrees[0].MissingFiles) != 3 || len(f.Worktrees[0].MissingDeps) != 1 {
		t.Fatalf("worktree manual deveria aparecer incompleta: %+v", f)
	}
	lg := &memLog{}
	if err := setupWorktree(ctx, cfg, &cfg.Repos[0], "manual", wt, false, 0, newPortAllocator(), lg); err != nil {
		t.Fatalf("setup: %v\n%s", err, lg)
	}
	f = findFeature(scanState(ctx, cfg), "manual")
	if len(f.Worktrees[0].MissingFiles) != 0 || len(f.Worktrees[0].MissingDeps) != 1 {
		t.Fatalf("setup não completou: %+v", f.Worktrees[0])
	}

	// Pasta apagada à mão vira registro quebrado até o prune.
	if err := removeTree(ctx, wt); err != nil {
		t.Fatal(err)
	}
	st := scanState(ctx, cfg)
	if len(st.Stale) != 1 || findFeature(st, "manual") != nil {
		t.Fatalf("stale = %+v", st.Stale)
	}
	if err := pruneRepos(ctx, cfg, nil, lg); err != nil {
		t.Fatal(err)
	}
	if st := scanState(ctx, cfg); len(st.Stale) != 0 {
		t.Fatalf("prune não limpou: %+v", st.Stale)
	}
}

func TestDeleteBranchSafeAndForced(t *testing.T) {
	cfg := fixture(t)
	ctx := context.Background()
	main := cfg.mainPath(&cfg.Repos[0])
	gitc := []string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}
	lg := &memLog{}

	// Branch com commit sem merge nem push: -d recusa e a branch fica.
	if err := createFeature(ctx, cfg, &CreateReq{Feature: "a", Repos: []CreateRepoReq{{Key: "app"}}}, newPortAllocator(), lg); err != nil {
		t.Fatalf("create: %v\n%s", err, lg)
	}
	wtA := filepath.Join(cfg.Root, "acme-app", "dev.a")
	write(t, filepath.Join(wtA, "x.txt"), "x")
	run(t, wtA, "git", "add", "x.txt")
	run(t, wtA, "git", append(gitc, "commit", "-q", "-m", "local")...)
	branchA := "dev.a." + time.Now().Format("020106")
	if err := removeFeature(ctx, cfg, &RemoveReq{Feature: "a", Force: true, DeleteBranch: true}, lg); err != nil {
		t.Fatalf("remove: %v\n%s", err, lg)
	}
	if !refExists(ctx, main, "refs/heads/"+branchA) {
		t.Fatal("-d não deveria apagar branch sem merge")
	}

	// Branch existente vinda do origin: -D apaga a local e o remoto continua.
	if err := createFeature(ctx, cfg, &CreateReq{Feature: "b", Repos: []CreateRepoReq{{Key: "app", Mode: "existing", Branch: "origin/outra"}}}, newPortAllocator(), lg); err != nil {
		t.Fatalf("create: %v\n%s", err, lg)
	}
	wtB := filepath.Join(cfg.Root, "acme-app", "dev.b")
	write(t, filepath.Join(wtB, "y.txt"), "y")
	run(t, wtB, "git", "add", "y.txt")
	run(t, wtB, "git", append(gitc, "commit", "-q", "-m", "local")...)
	if err := removeFeature(ctx, cfg, &RemoveReq{Feature: "b", Force: true, DeleteBranch: true, ForceBranch: true}, lg); err != nil {
		t.Fatalf("remove: %v\n%s", err, lg)
	}
	if refExists(ctx, main, "refs/heads/outra") {
		t.Fatal("-D deveria apagar a branch local")
	}
	out, err := git(ctx, main, "ls-remote", "--heads", "origin", "outra")
	if err != nil || !strings.Contains(out, "refs/heads/outra") {
		t.Fatalf("a branch do remoto não pode ser tocada: %q %v", out, err)
	}
}
