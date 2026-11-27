package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	tmp := realPath(t.TempDir())
	t.Setenv("WT_STATE_DIR", filepath.Join(tmp, "state"))
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

func TestIgnoredLossesBeforeRemove(t *testing.T) {
	cfg := fixture(t)
	ctx := context.Background()
	if err := createFeature(ctx, cfg, &CreateReq{Feature: "ign", Repos: []CreateRepoReq{{Key: "app"}}}, newPortAllocator(), &memLog{}); err != nil {
		t.Fatal(err)
	}
	rc := cfg.repo("app")
	main := cfg.mainPath(rc)
	wt := filepath.Join(cfg.Root, "acme-app", "dev.ign")
	write(t, filepath.Join(main, ".git/info/exclude"), "*.sqlite\nstorage/\n")
	write(t, filepath.Join(wt, "db.sqlite"), "x")
	write(t, filepath.Join(wt, "storage/logs/a.log"), "x")
	write(t, filepath.Join(wt, "node_modules/pkg/index.js"), "x")

	names := func() map[string]string {
		t.Helper()
		got, err := ignoredLosses(ctx, cfg, rc, wt)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		for _, f := range got.Files {
			m[f.Name] = f.Note
		}
		return m
	}
	// .env e docker/dev/.env saíram iguais ao main (fora projeto e porta): não contam.
	want := map[string]string{"db.sqlite": "", "storage": "pasta", ".claude": "copiada do main"}
	if got := names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ignorados = %v, quer %v", got, want)
	}
	write(t, filepath.Join(wt, ".env"), "FAKE_SECRET=mudou\n")
	if got := names(); got[".env"] != "difere do main" {
		t.Fatalf(".env editado não avisado: %v", got)
	}

	s := newServer(cfg)
	get := func(path string) (int, string) {
		req := httptest.NewRequest("GET", "/api/ignored?"+url.Values{"repo": {"app"}, "path": {path}}.Encode(), nil)
		req.Host = "127.0.0.1:7777"
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	if code, body := get(wt); code != http.StatusOK || !strings.Contains(body, `"db.sqlite"`) || strings.Contains(body, "mudou") {
		t.Fatalf("GET worktree = %d %s", code, body)
	}
	for _, p := range []string{main, filepath.Join(cfg.Root, "acme-app"), "/etc"} {
		if code, _ := get(p); code != http.StatusBadRequest {
			t.Fatalf("GET %s = %d, quer 400", p, code)
		}
	}

	lg := &memLog{}
	if err := removeFeature(ctx, cfg, &RemoveReq{Feature: "ign", SkipSnapshot: true}, lg); err != nil {
		t.Fatalf("remove: %v\n%s", err, lg)
	}
	if !strings.Contains(lg.String(), "saíram junto") || !strings.Contains(lg.String(), "db.sqlite") {
		t.Fatalf("aviso de ignorados ausente:\n%s", lg)
	}
}

func createHTTP(t *testing.T, s *server, feature string) (code int, ok bool) {
	t.Helper()
	body := jsonBody(t, CreateReq{Feature: feature, Repos: []CreateRepoReq{{Key: "app"}}})
	w := apiCall(t, s, http.MethodPost, "/api/create", body)
	if w.Code != http.StatusOK {
		return w.Code, false
	}
	ok, _ = ndjsonDone(t, w)
	return w.Code, ok
}

// together roda fn(i) em n goroutines soltas ao mesmo tempo.
func together(n int, fn func(i int)) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			fn(i)
		}(i)
	}
	close(start)
	wg.Wait()
}

func worktreeCount(t *testing.T, cfg *Config) int {
	t.Helper()
	wts, err := listWorktrees(context.Background(), cfg.mainPath(&cfg.Repos[0]))
	if err != nil {
		t.Fatal(err)
	}
	return len(wts)
}

func devPort(t *testing.T, cfg *Config, feature string) string {
	t.Helper()
	v, _ := getEnvValue(read(t, filepath.Join(cfg.Root, "acme-app", "dev."+feature, "docker/dev/.env")), "DEV_PORT")
	return v
}

func TestConcurrentCreateSameFeature(t *testing.T) {
	cfg := fixture(t)
	s := newServer(cfg)
	const n = 6
	codes, oks := make([]int, n), make([]bool, n)
	together(n, func(i int) { codes[i], oks[i] = createHTTP(t, s, "igual") })
	won := 0
	for i := range codes {
		switch {
		case codes[i] == http.StatusOK && oks[i]:
			won++
		case codes[i] != http.StatusConflict:
			t.Fatalf("pedido %d: status %d ok=%v", i, codes[i], oks[i])
		}
	}
	if won != 1 || worktreeCount(t, cfg) != 2 {
		t.Fatalf("venceram %d, worktrees %d; quer 1 e 2 (main + nova)", won, worktreeCount(t, cfg))
	}
}

// Duas instâncias no mesmo StateDir (outra porta, go run) escolhiam a mesma
// porta do docker: cada uma só enxerga a reserva dela.
func TestTwoInstancesDoNotShareDockerPort(t *testing.T) {
	cfg := fixture(t)
	srv := []*server{newServer(cfg), newServer(cfg)}
	feat := []string{"fa", "fb"}
	codes, oks := make([]int, 2), make([]bool, 2)
	together(2, func(i int) { codes[i], oks[i] = createHTTP(t, srv[i], feat[i]) })
	var won, lost int
	switch {
	case oks[0] && codes[1] == http.StatusConflict:
		won, lost = 0, 1
	case oks[1] && codes[0] == http.StatusConflict:
		won, lost = 1, 0
	default:
		t.Fatalf("quer uma criada e um 409; codes=%v oks=%v", codes, oks)
	}
	if exists(filepath.Join(cfg.Root, "acme-app", "dev."+feat[lost])) {
		t.Fatal("a instância barrada não pode criar nada")
	}
	if code, ok := createHTTP(t, srv[lost], feat[lost]); code != http.StatusOK || !ok {
		t.Fatalf("depois de liberada: status %d ok=%v", code, ok)
	}
	if a, b := devPort(t, cfg, feat[won]), devPort(t, cfg, feat[lost]); a == b {
		t.Fatalf("as duas worktrees ficaram com DEV_PORT=%s", a)
	}
}

// Um git de fora disputa a mesma pasta e branch: quem perde sai com erro e
// ninguém configura a worktree do outro.
func TestCreateRacesOutsideGit(t *testing.T) {
	cfg := fixture(t)
	main := cfg.mainPath(&cfg.Repos[0])
	ctx := context.Background()
	for round := 0; round < 4; round++ {
		feature := "fora" + string(rune('a'+round))
		wt := filepath.Join(cfg.Root, "acme-app", "dev."+feature)
		branch := cfg.branchName(feature, time.Now())
		var ours, theirs error
		together(2, func(i int) {
			if i == 0 {
				req := &CreateReq{Feature: feature, Repos: []CreateRepoReq{{Key: "app"}}}
				ours = createFeature(ctx, cfg, req, newPortAllocator(), &memLog{})
				return
			}
			_, theirs = git(ctx, main, "worktree", "add", "--no-track", "-b", branch, wt, "origin/dev")
		})
		if ours == nil && theirs == nil {
			t.Fatalf("rodada %d: os dois criaram %s", round, wt)
		}
		denv := filepath.Join(wt, "docker/dev/.env")
		if ours == nil && !exists(denv) {
			t.Fatalf("rodada %d: venceu e não configurou", round)
		}
		if ours != nil && exists(denv) {
			t.Fatalf("rodada %d: perdeu (%v) e mexeu na worktree do outro", round, ours)
		}
	}
	if got, want := worktreeCount(t, cfg), 5; got != want {
		t.Fatalf("worktrees = %d, quer %d", got, want)
	}
}

// TestHoldOpLock não testa nada sozinho: é o processo filho que segura a
// trava em TestOpLockAcrossProcesses.
func TestHoldOpLock(t *testing.T) {
	p := os.Getenv("WT_HOLD_OP_LOCK")
	if p == "" {
		t.Skip("só roda como processo filho")
	}
	unlock, err := lockFile(p)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	os.Stdout.WriteString("travado\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestOpLockAcrossProcesses(t *testing.T) {
	cfg := fixture(t)
	s := newServer(cfg)
	if err := os.MkdirAll(stateRoot(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	hold := func() (*exec.Cmd, io.WriteCloser) {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(self, "-test.run=^TestHoldOpLock$")
		cmd.Env = append(os.Environ(), "WT_HOLD_OP_LOCK="+filepath.Join(stateRoot(cfg), "op.lock"))
		in, _ := cmd.StdinPipe()
		out, _ := cmd.StdoutPipe()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		line, _ := bufio.NewReader(out).ReadString('\n')
		if line != "travado\n" {
			t.Fatalf("filho não travou: %q", line)
		}
		return cmd, in
	}
	free := func() bool {
		release, ok := s.tryOp()
		if ok {
			release()
		}
		return ok
	}

	cmd, in := hold()
	if free() {
		t.Fatal("trava de outro processo ignorada")
	}
	if code, _ := createHTTP(t, s, "barrada"); code != http.StatusConflict {
		t.Fatalf("create com outro processo operando: status %d", code)
	}
	if st := apiCall(t, s, http.MethodGet, "/api/state", ""); !strings.Contains(st.Body.String(), `"busy":true`) {
		t.Fatalf("estado não marcou busy: %s", st.Body.String())
	}
	_ = in.Close()
	_ = cmd.Wait()
	if !free() {
		t.Fatal("trava não saiu com o fim do processo")
	}

	// Processo morto no meio da operação também solta.
	cmd, _ = hold()
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if !free() {
		t.Fatal("trava ficou presa depois do kill")
	}
}
