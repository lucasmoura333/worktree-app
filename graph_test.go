package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func graphFixture(t *testing.T) *server {
	t.Helper()
	vault, docs := t.TempDir(), t.TempDir()
	write(t, filepath.Join(docs, "kanbans/frente.md"), "- [x] card -> [[worktree-app/docs/savepoints/2026-11-02-decl|Savepoint]]\n")
	write(t, filepath.Join(docs, "savepoints/2026-11-02-decl.md"), "---\ndate: 2026-11-02\nbranch: dev.sinais\n---\nVer [[11-02-2026]].\n")
	write(t, filepath.Join(docs, "savepoints/2026-11-03-cita.md"), "---\ndate: 2026-11-03\n---\nMexi em dev.sinais hoje.\n")
	write(t, filepath.Join(docs, "savepoints/2026-11-04-outro.md"), "---\ndate: 2026-11-04\n---\nNada a ver: dev.sinais-2\n")
	write(t, filepath.Join(docs, "savepoints/_INDEX.md"), "[[2026-11-02-decl]] [[2026-11-03-cita]] [[2026-11-04-outro]]\n")
	write(t, filepath.Join(vault, "worktree-app/docs/savepoints/so-no-espelho.md"), "espelho velho, ignorado\n")
	write(t, filepath.Join(vault, "worktree-app/Diario/2026/11 - Novembro/11-02-2026.md"), "# Diário\n")
	write(t, filepath.Join(vault, "worktree-app/worktrees/sinais--app.md"), string(renderNote(vaultNote{WT: WorktreeInfo{Feature: "sinais", Repo: "app", RepoDir: "acme-app", Branch: "dev.sinais", Connection: &Connection{}}, Status: "ativo"})))
	write(t, filepath.Join(vault, "Pessoal/pessoal.md"), "[[worktree-app/worktrees/sinais--app]]\n")
	cfg := &Config{Root: t.TempDir(), Listen: "127.0.0.1:7777", Vault: &VaultConfig{Path: vault, Docs: docs}, Repos: []RepoConfig{{Key: "app", Dir: "acme-app"}}}
	applyDefaults(cfg)
	return newServer(cfg)
}

func getGraph(t *testing.T, s *server, query string) (graphResp, map[string]graphNode) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/graph?"+query, nil)
	req.Host = "127.0.0.1:7777"
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	var g graphResp
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil || rec.Code != 200 {
		t.Fatalf("%d %v %s", rec.Code, err, rec.Body.String())
	}
	byID := map[string]graphNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	return g, byID
}

func TestGraphLocalDepthMentionsAndScope(t *testing.T) {
	s := graphFixture(t)
	wt := "worktree-app/worktrees/sinais--app"
	sp := "worktree-app/docs/savepoints/"
	g, nodes := getGraph(t, s, "seed="+url.QueryEscape(wt))
	if nodes[wt].Level != 0 || nodes[sp+"2026-11-02-decl"].Level != 1 || nodes[sp+"2026-11-03-cita"].Level != 1 {
		t.Fatalf("profundidade 1: %+v", g.Nodes)
	}
	for id := range nodes {
		if strings.Contains(id, "outro") || strings.Contains(id, "Pessoal") || strings.Contains(id, "_INDEX") || strings.Contains(id, "kanbans") || strings.Contains(id, "espelho") {
			t.Errorf("%s não devia estar na profundidade 1", id)
		}
	}
	kinds := map[string]string{}
	for _, e := range g.Edges {
		kinds[e.From+">"+e.To] = e.Kind
	}
	if kinds[sp+"2026-11-02-decl>"+wt] != "link" || kinds[sp+"2026-11-03-cita>"+wt] != "mention" && kinds[wt+">"+sp+"2026-11-03-cita"] != "mention" {
		t.Fatalf("arestas: %v", kinds)
	}

	_, nodes = getGraph(t, s, "depth=2&seed="+url.QueryEscape(wt))
	if nodes["worktree-app/docs/kanbans/frente"].Level != 2 {
		t.Fatalf("kanban na profundidade 2: %+v", nodes)
	}
	if _, ok := nodes["worktree-app/Diario/2026/11 - Novembro/11-02-2026"]; ok {
		t.Fatal("diário só entra com diario=1")
	}
	_, nodes = getGraph(t, s, "depth=2&diario=1&seed="+url.QueryEscape(wt))
	if _, ok := nodes["worktree-app/Diario/2026/11 - Novembro/11-02-2026"]; !ok {
		t.Fatal("diário com diario=1")
	}
	_, nodes = getGraph(t, s, "mentions=0&seed="+url.QueryEscape(wt))
	if _, ok := nodes[sp+"2026-11-03-cita"]; ok {
		t.Fatal("menção com mentions=0")
	}
	g, _ = getGraph(t, s, "seed=Pessoal/pessoal&seed=../x")
	if len(g.Nodes) != 0 || len(g.Missing) != 2 {
		t.Fatalf("fora do escopo: %+v", g)
	}
}
