package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// seedOf devolve o repo "seed" do fixture, que tem push no origin.
func seedOf(cfg *Config) string { return filepath.Join(filepath.Dir(cfg.Root), "seed") }

func branchNames(t *testing.T, s *server, repo string) map[string]Branch {
	t.Helper()
	w := apiCall(t, s, http.MethodGet, "/api/branches?repo="+repo, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/branches: %d %s", w.Code, w.Body.String())
	}
	var list []Branch
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	out := map[string]Branch{}
	for _, b := range list {
		key := b.Name
		if b.Remote {
			key = "remote:" + b.Name
		}
		out[key] = b
	}
	return out
}

func TestBranchesEndpoint(t *testing.T) {
	cfg := fixture(t)
	s := newServer(cfg)

	got := branchNames(t, s, "app")
	if _, ok := got["dev"]; !ok {
		t.Fatalf("faltou a branch local dev: %v", got)
	}
	found := false
	for k := range got {
		if strings.HasPrefix(k, "remote:") && strings.HasSuffix(k, "outra") {
			found = true
		}
	}
	if !found {
		t.Fatalf("faltou a remota outra: %v", got)
	}
	for _, b := range got {
		if b.Date == "" {
			t.Fatalf("branch sem data: %+v", b)
		}
	}

	if w := apiCall(t, s, http.MethodGet, "/api/branches?repo=nao-existe", ""); w.Code != http.StatusNotFound {
		t.Fatalf("repo desconhecido: %d", w.Code)
	}
}

func TestPlanEndpoint(t *testing.T) {
	cfg := fixture(t)
	s := newServer(cfg)

	post := func(body string) (int, Plan) {
		w := apiCall(t, s, http.MethodPost, "/api/plan", body)
		var p Plan
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
		}
		return w.Code, p
	}

	code, p := post(`{"feature":"plano-ok","repos":[{"key":"app"}]}`)
	if code != http.StatusOK || !p.OK || len(p.Items) != 1 {
		t.Fatalf("plano válido: %d %+v", code, p)
	}
	it := p.Items[0]
	if it.Repo != "app" || it.Path == "" || it.Branch == "" || len(it.Problems) != 0 {
		t.Fatalf("item inesperado: %+v", it)
	}
	// O plano não cria nada no disco.
	if exists(it.Path) {
		t.Fatalf("o plano criou %s", it.Path)
	}

	if _, p := post(`{"feature":"../fora","repos":[{"key":"app"}]}`); p.OK || len(p.Problems) == 0 {
		t.Fatalf("feature inválida deveria ter problema: %+v", p)
	}
	if _, p := post(`{"feature":"sem-repo","repos":[]}`); p.OK || len(p.Problems) == 0 {
		t.Fatalf("plano sem repo deveria ter problema: %+v", p)
	}
	if code, _ := post(`{"feature":"x","campo":1}`); code != http.StatusBadRequest {
		t.Fatalf("campo desconhecido: %d", code)
	}
	if code, _ := post(`não é json`); code != http.StatusBadRequest {
		t.Fatalf("json inválido: %d", code)
	}
}

func TestOpenEndpointValidatesPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("usa o comando true como editor")
	}
	cfg := fixture(t)
	cfg.Editor = []string{"true"}
	s := newServer(cfg)

	open := func(path string) int {
		return apiCall(t, s, http.MethodPost, "/api/open", jsonBody(t, map[string]string{"path": path})).Code
	}

	if c := open("dev.code-workspace"); c != http.StatusOK {
		t.Fatalf("caminho relativo ao root: %d", c)
	}
	if c := open(filepath.Join(cfg.Root, "acme-app", "dev")); c != http.StatusOK {
		t.Fatalf("caminho absoluto dentro do root: %d", c)
	}

	weird := filepath.Join(cfg.Root, "a&b")
	if err := os.MkdirAll(weird, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		t.TempDir(),                 // fora do root
		"../",                       // sobe do root
		"nao-existe.code-workspace", // não existe
		"a&b",                       // caractere que o cmd.exe interpretaria
		filepath.Join(cfg.Root, "..", filepath.Base(cfg.Root)+"-irmao"), // prefixo parecido
	} {
		if c := open(p); c != http.StatusBadRequest {
			t.Fatalf("%q deveria ser recusado, veio %d", p, c)
		}
	}
	if c := apiCall(t, s, http.MethodPost, "/api/open", `{"path":"x","extra":true}`).Code; c != http.StatusBadRequest {
		t.Fatalf("campo desconhecido: %d", c)
	}
}

func TestSetupEndpoint(t *testing.T) {
	cfg := fixture(t)
	cfg.StateDir = t.TempDir()
	wt := createForSnapshot(t, cfg, "setuphttp")
	s := newServer(cfg)

	// Apaga um arquivo copiado: o setup deve trazê-lo de volta.
	envPath := filepath.Join(wt.Path, ".env")
	if err := os.Remove(envPath); err != nil {
		t.Fatal(err)
	}
	ok, errs := ndjsonDone(t, apiCall(t, s, http.MethodPost, "/api/setup",
		jsonBody(t, map[string]any{"repo": "app", "path": wt.Path, "install": false})))
	if !ok {
		t.Fatalf("setup falhou: %v", errs)
	}
	if got := read(t, envPath); got != "FAKE_SECRET=nao-e-real\n" {
		t.Fatalf(".env não voltou: %q", got)
	}

	for _, body := range []string{
		jsonBody(t, map[string]any{"repo": "nao-existe", "path": wt.Path}),
		jsonBody(t, map[string]any{"repo": "app", "path": filepath.Join(cfg.Root, "acme-app", "dev")}), // a main não é alvo
		jsonBody(t, map[string]any{"repo": "app", "path": t.TempDir()}),
		`{"repo":"app","path":"x","oops":1}`,
	} {
		if ok, _ := ndjsonDone(t, apiCall(t, s, http.MethodPost, "/api/setup", body)); ok {
			t.Fatalf("setup deveria falhar: %s", body)
		}
	}
}

func TestPruneAndFetchEndpoints(t *testing.T) {
	cfg := fixture(t)
	cfg.StateDir = t.TempDir()
	wt := createForSnapshot(t, cfg, "prunehttp")
	s := newServer(cfg)
	main := filepath.Join(cfg.Root, "acme-app", "dev")

	// Prune: a pasta some por fora e o registro da worktree é limpo.
	if err := os.RemoveAll(wt.Path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gitOut(t, main, "worktree", "list", "--porcelain"), filepath.Base(wt.Path)) {
		t.Fatal("o git deveria ainda listar a worktree apagada")
	}
	ok, errs := ndjsonDone(t, apiCall(t, s, http.MethodPost, "/api/prune", `{"repos":["app"]}`))
	if !ok {
		t.Fatalf("prune falhou: %v", errs)
	}
	if strings.Contains(gitOut(t, main, "worktree", "list", "--porcelain"), filepath.Base(wt.Path)) {
		t.Fatal("prune não limpou a worktree apagada")
	}

	// Fetch: uma branch nova no origin aparece em /api/branches.
	seed := seedOf(cfg)
	run(t, seed, "git", "checkout", "-q", "-b", "nova-remota")
	run(t, seed, "git", "push", "-q", "origin", "nova-remota")
	has := func() bool {
		for k := range branchNames(t, s, "app") {
			if strings.HasPrefix(k, "remote:") && strings.HasSuffix(k, "nova-remota") {
				return true
			}
		}
		return false
	}
	if has() {
		t.Fatal("a branch não deveria aparecer antes do fetch")
	}
	ok, errs = ndjsonDone(t, apiCall(t, s, http.MethodPost, "/api/fetch", `{"repos":[]}`))
	if !ok {
		t.Fatalf("fetch falhou: %v", errs)
	}
	if !has() {
		t.Fatal("a branch nova não apareceu depois do fetch")
	}

	// Fetch com o origin quebrado termina com erro, sem travar.
	run(t, main, "git", "remote", "set-url", "origin", filepath.Join(t.TempDir(), "sumiu.git"))
	if ok, _ := ndjsonDone(t, apiCall(t, s, http.MethodPost, "/api/fetch", `{"repos":["app"]}`)); ok {
		t.Fatal("fetch com origin inexistente deveria falhar")
	}
	if ok, _ := ndjsonDone(t, apiCall(t, s, http.MethodPost, "/api/prune", `{"repos":"app"}`)); ok {
		t.Fatal("prune com corpo inválido deveria falhar")
	}
}
