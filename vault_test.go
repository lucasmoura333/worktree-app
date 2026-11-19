package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func vaultFixture(t *testing.T) (*server, *State, string) {
	t.Helper()
	root := t.TempDir()
	cfg := &Config{Vault: &VaultConfig{Path: root}, Repos: []RepoConfig{{Key: "app", Dir: "acme-app"}}}
	s := newServer(cfg)
	st := &State{Repos: []RepoState{{Key: "app", Dir: "acme-app"}}, Features: []FeatureState{{Name: "checkout", Worktrees: []WorktreeInfo{{Feature: "checkout", Repo: "app", RepoDir: "acme-app", Branch: "dev.checkout", Head: "1a2b3c4d5", Dirty: 2, Upstream: "origin/dev", Behind: 3, Port: 5183}}}}}
	return s, st, filepath.Join(root, filepath.FromSlash(vaultFolder))
}
func TestVaultLifecycleAndPrivacy(t *testing.T) {
	s, st, dir := vaultFixture(t)
	s.syncVault(st)
	if st.VaultError != "" {
		t.Fatal(st.VaultError)
	}
	path := filepath.Join(dir, "checkout--app.md")
	n, err := readNote(path)
	if err != nil {
		t.Fatal(err)
	}
	n.WT.Connection.Front = "[[worktree-app/docs/kanbans/notificacoes]]"
	n.WT.Connection.LastSavepoint = "[[worktree-app/docs/savepoints/SP-10]]"
	if err := writeNote(dir, n); err != nil {
		t.Fatal(err)
	}
	st.Features[0].Worktrees[0].MissingFiles = []string{"private-name.env"}
	s.syncVault(st)
	if st.VaultError != "" {
		t.Fatal(st.VaultError)
	}
	b := read(t, path)
	if strings.Contains(b, "private-name") || !strings.Contains(b, "notificacoes") {
		t.Fatal("lost links or leaked filenames")
	}
	var canvas map[string]any
	if err := json.Unmarshal([]byte(read(t, filepath.Join(dir, "worktrees.canvas"))), &canvas); err != nil {
		t.Fatal(err)
	}
	if len(canvas["nodes"].([]any)) != 3 {
		t.Fatal("expected front/worktree/savepoint nodes")
	}
	st.Features = nil
	s.syncVault(st)
	if st.VaultError != "" {
		t.Fatal(st.VaultError)
	}
	if len(st.Archived) != 1 || st.Archived[0].Head != "1a2b3c4d5" {
		t.Fatal("lost removal SHA")
	}
	n, err = readNote(path)
	if err != nil || n.Status != "removido" {
		t.Fatal("note must be retained as removed", err)
	}
}
func TestVaultDoesNotArchiveOnRepoFailureOrStale(t *testing.T) {
	s, st, dir := vaultFixture(t)
	s.syncVault(st)
	wt := st.Features[0].Worktrees[0]
	st.Features = nil
	st.Repos[0].Error = "unavailable"
	s.syncVault(st)
	n, _ := readNote(filepath.Join(dir, "checkout--app.md"))
	if n.Status != "ativo" {
		t.Fatal("repo failure archived a note")
	}
	st.Repos[0].Error = ""
	st.Stale = []WorktreeInfo{wt}
	s.syncVault(st)
	n, _ = readNote(filepath.Join(dir, "checkout--app.md"))
	if n.Status != "ativo" {
		t.Fatal("stale worktree was archived")
	}
}
func TestVaultPreservesManualFiles(t *testing.T) {
	s, st, dir := vaultFixture(t)
	path := filepath.Join(dir, "checkout--app.md")
	write(t, path, "# Manual\nDo not overwrite\n")
	s.syncVault(st)
	if st.VaultError == "" || read(t, path) != "# Manual\nDo not overwrite\n" {
		t.Fatal("manual file overwritten")
	}
}
func TestVaultRejectsTraversalAndEscapesYAML(t *testing.T) {
	for _, v := range []string{"../../secret", "[[worktree-app/../secret]]", "worktree-app/test\nvalue", "https://example.com"} {
		if _, err := normalizeWiki(v); err == nil {
			t.Fatal("accepted unsafe link", v)
		}
	}
	s, st, dir := vaultFixture(t)
	st.Features[0].Worktrees[0].Feature = "../../outside"
	st.Features[0].Worktrees[0].Branch = "branch\"\nsecret: injected"
	s.syncVault(st)
	if st.VaultError != "" {
		t.Fatal(st.VaultError)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			n, err := readNote(filepath.Join(dir, e.Name()))
			if err != nil || n.WT.Branch != "branch\"\nsecret: injected" {
				t.Fatal("YAML failed to round trip", err)
			}
		}
	}
	if safeNoteName("a--b", "c") == safeNoteName("a", "b--c") {
		t.Fatal("note identity collision")
	}
}

func TestVaultHTTPCreateLinkAndRemove(t *testing.T) {
	cfg := fixture(t)
	if resolved, err := filepath.EvalSymlinks(cfg.Root); err == nil {
		cfg.Root = resolved
	}
	cfg.Repos[0].DockerEnv = nil // This integration tests note lifecycle, independent of host port reservations.
	cfg.Vault = &VaultConfig{Path: t.TempDir()}
	s := newServer(cfg)
	handler := s.routes()
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(method, "http://"+cfg.Listen+path, bytes.NewReader(b))
		req.Header.Set("X-WT", "1")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	created := call(http.MethodPost, "/api/create", CreateReq{Feature: "linked", Repos: []CreateRepoReq{{Key: "app"}}})
	if !strings.Contains(created.Body.String(), `"ok":true`) {
		t.Fatal(created.Body.String())
	}
	res := call(http.MethodGet, "/api/state", nil)
	var st State
	if err := json.Unmarshal(res.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.VaultError != "" || len(st.Features) != 1 || st.Features[0].Worktrees[0].Connection == nil {
		t.Fatalf("missing note after create: %#v", st)
	}
	originalSHA := st.Features[0].Worktrees[0].Head
	linked := call(http.MethodPost, "/api/connections", map[string]string{"repo": "app", "feature": "linked", "front": "worktree-app/docs/kanbans/example", "lastSavepoint": "[[worktree-app/docs/savepoints/example]]"})
	if linked.Code != 200 {
		t.Fatal(linked.Body.String())
	}
	removed := call(http.MethodPost, "/api/remove", RemoveReq{Feature: "linked", Repos: []string{"app"}, Force: true, DeleteBranch: true, ForceBranch: true})
	if !strings.Contains(removed.Body.String(), `"ok":true`) {
		t.Fatal(removed.Body.String())
	}
	res = call(http.MethodGet, "/api/state", nil)
	if err := json.Unmarshal(res.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Archived) != 1 || st.Archived[0].Head != originalSHA || !strings.Contains(st.Archived[0].Connection.Front, "example") {
		t.Fatal("lost SHA or links after deleting branch")
	}
}

func TestDemoRoutesAreReadOnly(t *testing.T) {
	cfg := &Config{Listen: "127.0.0.1:7788"}
	handler := demoHandler(cfg)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:7788/api/remove", strings.NewReader(`{}`))
	req.Header.Set("X-WT", "1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("demo allowed mutation: %d", rec.Code)
	}
}
