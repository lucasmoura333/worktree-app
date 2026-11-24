package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func getDiff(t *testing.T, s *server, q url.Values) (int, DiffResp, string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/diff?"+q.Encode(), nil)
	req.Host = "127.0.0.1:7777"
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, req)
	var d DiffResp
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
	}
	return w.Code, d, w.Body.String()
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestDiffAgainstBaseAndOtherWorktree(t *testing.T) {
	cfg := fixture(t)
	ctx := context.Background()
	for _, f := range []string{"a", "b"} {
		if err := createFeature(ctx, cfg, &CreateReq{Feature: f, Repos: []CreateRepoReq{{Key: "app"}}}, newPortAllocator(), &memLog{}); err != nil {
			t.Fatal(err)
		}
	}
	wa := filepath.Join(cfg.Root, "acme-app", "dev.a")
	wb := filepath.Join(cfg.Root, "acme-app", "dev.b")
	gitc := []string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}
	write(t, filepath.Join(wa, "src/feito.txt"), "commitado\n")
	run(t, wa, "git", "add", ".")
	run(t, wa, "git", append(gitc, "commit", "-q", "-m", "feito")...)
	write(t, filepath.Join(wa, "yarn.lock"), "lock-v1\nalterado-sem-commit\n")
	write(t, filepath.Join(wa, "novo-sem-add.txt"), "x\n")
	write(t, filepath.Join(wa, ".env"), "FAKE_SECRET=mudou\n") // está no copy: nunca aparece
	write(t, filepath.Join(wb, "outro.txt"), "b\n")
	run(t, wb, "git", "add", ".")
	run(t, wb, "git", append(gitc, "commit", "-q", "-m", "b")...)

	statusBefore := gitOut(t, wa, "status", "--porcelain")
	s := newServer(cfg)

	code, d, body := getDiff(t, s, url.Values{"repo": {"app"}, "path": {wa}, "against": {"base"}})
	if code != 200 {
		t.Fatalf("base: %d %s", code, body)
	}
	names := map[string]DiffFile{}
	for _, f := range d.Files {
		names[f.Path] = f
	}
	if _, ok := names["src/feito.txt"]; !ok {
		t.Fatalf("commit não apareceu: %+v", d.Files)
	}
	if f, ok := names["yarn.lock"]; !ok || f.Add != 1 {
		t.Fatalf("alteração sem commit não apareceu: %+v", d.Files)
	}
	if strings.Contains(d.Patch, "FAKE_SECRET") || strings.Contains(strings.Join(d.Untracked, ","), ".env") {
		t.Fatal(".env vazou no diff")
	}
	if strings.Join(d.Untracked, ",") != "novo-sem-add.txt" {
		t.Fatalf("untracked: %v", d.Untracked)
	}
	if !d.WorkingDir || d.DirtyFrom == 0 {
		t.Fatalf("meta: %+v", d)
	}

	code, d, body = getDiff(t, s, url.Values{"repo": {"app"}, "path": {wa}, "against": {wb}})
	if code != 200 {
		t.Fatalf("a contra b: %d %s", code, body)
	}
	got := map[string]DiffFile{}
	for _, f := range d.Files {
		got[f.Path] = f
	}
	if got["src/feito.txt"].Add != 1 || got["outro.txt"].Del != 1 {
		t.Fatalf("a contra b: %+v", d.Files)
	}
	if _, ok := got["yarn.lock"]; ok {
		t.Fatal("entre worktrees só entram commits")
	}

	s.cfg.DiffMaxKB = 0 // corte mínimo: 0 KB
	code, d, _ = getDiff(t, s, url.Values{"repo": {"app"}, "path": {wa}, "against": {"base"}})
	if code != 200 || !d.Truncated || d.Patch != "" {
		t.Fatalf("corte: %d truncated=%v len=%d", code, d.Truncated, len(d.Patch))
	}

	for _, q := range []url.Values{
		{"repo": {"app"}, "path": {cfg.Root}},
		{"repo": {"nao"}, "path": {wa}},
		{"repo": {"app"}, "path": {wa}, "against": {"/etc"}},
	} {
		if code, _, _ := getDiff(t, s, q); code != http.StatusBadRequest {
			t.Errorf("%v: %d", q, code)
		}
	}

	if exists(filepath.Join(cfg.Root, "acme-app", "dev", ".git", "worktrees", "dev.a", "index.lock")) {
		t.Fatal("index.lock criado")
	}
	if after := gitOut(t, wa, "status", "--porcelain"); after != statusBefore {
		t.Fatalf("status mudou:\n%s\n%s", statusBefore, after)
	}
}

func TestParseNumstatZRename(t *testing.T) {
	files, cut := parseNumstatZ("1\t2\ta.txt\x00-\t-\tbin.png\x003\t0\t\x00velho.go\x00novo.go\x00", 10)
	if cut || len(files) != 3 || files[1].Binary != true || files[2].OldPath != "velho.go" || files[2].Path != "novo.go" || files[2].Add != 3 {
		t.Fatalf("%+v", files)
	}
	if _, cut := parseNumstatZ("1\t1\ta\x001\t1\tb\x00", 1); !cut {
		t.Fatal("limite de arquivos")
	}
}
