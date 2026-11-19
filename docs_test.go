package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func docFixture(t *testing.T) (*server, string, string) {
	t.Helper()
	vault, docs := t.TempDir(), t.TempDir()
	write(t, filepath.Join(vault, "worktree-app/docs/kanbans/frente.md"), "# espelho velho\n")
	write(t, filepath.Join(docs, "kanbans/frente.md"), "# canônico\n")
	write(t, filepath.Join(vault, "worktree-app/Diario/2026/11 - Novembro/11-10-2026.md"), "# Diário\n")
	write(t, filepath.Join(vault, "Pessoal/privado/nota.md"), "sigiloso\n")
	write(t, filepath.Join(vault, "worktree-app/docs/assets/x/ignorado.md"), "asset\n")
	cfg := &Config{Root: t.TempDir(), Listen: "127.0.0.1:7777", Vault: &VaultConfig{Path: vault, Docs: docs}, Repos: []RepoConfig{{Key: "app", Dir: "acme-app"}}}
	applyDefaults(cfg)
	return newServer(cfg), vault, docs
}

func getDoc(t *testing.T, s *server, link string) (int, map[string]string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/doc?path="+url.QueryEscape(link), nil)
	req.Host = "127.0.0.1:7777"
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	var body map[string]string
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, body
}

func TestDocPrefersCanonicalAndResolvesNames(t *testing.T) {
	s, _, _ := docFixture(t)
	code, b := getDoc(t, s, "[[worktree-app/docs/kanbans/frente|Kanban]]")
	if code != 200 || !strings.Contains(b["text"], "canônico") || b["source"] != "docs" {
		t.Fatalf("canônico: %d %v", code, b)
	}
	code, b = getDoc(t, s, "11-10-2026#Segurança")
	if code != 200 || b["path"] != "worktree-app/Diario/2026/11 - Novembro/11-10-2026" || b["source"] != "vault" {
		t.Fatalf("nome curto: %d %v", code, b)
	}
	if code, _ := getDoc(t, s, "ignorado"); code != 404 {
		t.Fatalf("assets não entram no índice de nomes: %d", code)
	}
}

func TestDocRefusesOutsideScope(t *testing.T) {
	s, vault, _ := docFixture(t)
	if err := os.Symlink(filepath.Join(vault, "Pessoal/privado/nota.md"), filepath.Join(vault, "worktree-app/atalho.md")); err != nil {
		t.Skip("sem symlink:", err)
	}
	for _, link := range []string{
		"Pessoal/privado/nota",
		"pessoal", // nome curto fora do escopo não é indexado
		"worktree-app/../Pessoal/privado/nota",
		"worktree-app/atalho", // symlink para fora
		"/etc/passwd",
		"C:/Windows/win",
		"worktree-app/docs/kanbans/frente\x00",
	} {
		if code, b := getDoc(t, s, link); code == 200 {
			t.Errorf("%q deveria ser recusado, veio %v", link, b)
		}
	}
}

func TestDocNeedsLocalHost(t *testing.T) {
	s, _, _ := docFixture(t)
	req := httptest.NewRequest("GET", "/api/doc?path=worktree-app/docs/kanbans/frente", nil)
	req.Host = "evil.example:7777"
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("host estranho: %d", rec.Code)
	}
}

func TestWorktreeNoteMentions(t *testing.T) {
	s, vault, _ := docFixture(t)
	sp := t.TempDir()
	s.cfg.Vault.Savepoints, s.cfg.Vault.SavepointsLink = sp, "worktree-app/docs/savepoints"
	write(t, filepath.Join(sp, "2026-11-01-texto.md"), "---\ntitle: \"Só cita\"\ndate: 2026-11-01\n---\nTrabalho em dev.sinais.\n")
	write(t, filepath.Join(sp, "2026-10-01-vinculado.md"), "---\ndate: 2026-10-01\nbranch: dev.sinais\n---\n")
	write(t, filepath.Join(sp, "2026-11-05-parecido.md"), "---\ndate: 2026-11-05\n---\nOutra: dev.sinais-2 e feat/dev.sinais.\n")
	write(t, filepath.Join(sp, "_INDEX.md"), "dev.sinais\n")
	n := vaultNote{WT: WorktreeInfo{Feature: "sinais", Repo: "app", RepoDir: "acme-app", Branch: "dev.sinais", Connection: &Connection{}}, Status: "ativo"}
	dir := filepath.Join(vault, filepath.FromSlash(vaultFolder))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeNote(dir, n); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/doc?path="+url.QueryEscape("worktree-app/worktrees/sinais--app"), nil)
	req.Host = "127.0.0.1:7777"
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	var resp docResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != 200 {
		t.Fatalf("%d %v %s", rec.Code, err, rec.Body.String())
	}
	if len(resp.Mentions) != 2 || !resp.Mentions[0].Linked || resp.Mentions[1].Title != "Só cita" || resp.Mentions[1].Linked {
		t.Fatalf("menções: %+v", resp.Mentions)
	}
}
