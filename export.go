package main

// Exporta a prévia (-demo) como site estático, para hospedar no GitHub Pages:
// a página embutida, o estado de exemplo, as notas do Vault de examples/ e um
// índice do grafo. O static-demo.js troca as chamadas /api/* por esses arquivos;
// operações continuam desativadas, como na prévia.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed static-demo.js
var staticShim []byte

type staticGraph struct {
	Built    string                 `json:"built"`
	Nodes    map[string]staticNode  `json:"nodes"`
	Out      map[string][]string    `json:"out"`
	Mentions map[string][]string    `json:"mentions"`
	Nb       map[string][2][]string `json:"nb"` // vizinhos ordenados: [sem menções, com menções]
}

type staticNode struct {
	Label string `json:"label"`
	Kind  string `json:"kind"`
	Title string `json:"title,omitempty"`
}

// exportStatic grava o site em dir usando o Vault de exemplo em vaultDir.
func exportStatic(dir, vaultDir string) error {
	vaultDir, err := filepath.Abs(vaultDir)
	if err != nil {
		return err
	}
	if !isDir(vaultDir) {
		return fmt.Errorf("vault de exemplo %s não existe", vaultDir)
	}
	// Cópia temporária: as notas das worktrees de exemplo são geradas nela
	// (como o app faz no Vault real), nunca em examples/.
	tmp, err := os.MkdirTemp("", "wt-export-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := copyTree(vaultDir, tmp); err != nil {
		return err
	}
	vaultDir = tmp
	cfg := &Config{
		Root: os.TempDir(),
		Vault: &VaultConfig{
			Path:           vaultDir,
			Scope:          defaultVaultScope,
			Savepoints:     filepath.Join(vaultDir, defaultVaultScope, "docs", "savepoints"),
			SavepointsLink: defaultVaultScope + "/docs/savepoints",
		},
	}
	applyDefaults(cfg)
	s := newServer(cfg)

	// 1. Página embutida + shim.
	sub, _ := fs.Sub(webFS, "web")
	if err := fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		if p == "index.html" {
			b = []byte(strings.Replace(string(b), `<script src="md.js"></script>`,
				`<script src="static-demo.js"></script>`+"\n    "+`<script src="md.js"></script>`, 1))
		}
		return writeFile(filepath.Join(dir, p), b)
	}); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, "static-demo.js"), staticShim); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, ".nojekyll"), nil); err != nil {
		return err
	}

	// 2. Estado de exemplo, com as notas das worktrees escritas na cópia.
	st := demoState()
	s.syncVault(st)
	st.Demo, st.DocsPreview = true, true
	st.Root = "Prévia online · dados de exemplo"
	st.Vault, st.VaultError = "Obsidian Vault", ""
	st.Reader = cfg.readerState()
	if err := writeJSONFile(filepath.Join(dir, "api", "state.json"), st); err != nil {
		return err
	}

	// 3. Índice do grafo (o shim refaz a busca em largura no navegador).
	idx := s.graphIndex(true)
	g := staticGraph{Built: idx.built.Format("2006-01-02T15:04:05Z07:00"), Nodes: map[string]staticNode{}, Out: map[string][]string{}, Mentions: map[string][]string{}, Nb: map[string][2][]string{}}
	ids := make([]string, 0, len(idx.notes))
	for id, n := range idx.notes {
		ids = append(ids, id)
		g.Nodes[id] = staticNode{Label: n.Label, Kind: n.Kind, Title: n.Title}
		g.Out[id] = sortedKeys(n.out)
		if m := sortedKeys(idx.mentions[id]); len(m) > 0 {
			g.Mentions[id] = m
		}
		g.Nb[id] = [2][]string{idx.neighbors(id, false), idx.neighbors(id, true)}
	}
	sort.Strings(ids)
	if err := writeJSONFile(filepath.Join(dir, "api", "graph.json"), g); err != nil {
		return err
	}

	// 4. Notas do escopo, pelo mesmo handler do app (mesmo JSON do leitor).
	docs := map[string]string{} // id -> arquivo
	for _, id := range ids {
		if !strings.HasPrefix(id, docScope) {
			continue
		}
		rec := httptest.NewRecorder()
		s.handleDoc(rec, httptest.NewRequest(http.MethodGet, "/api/doc?path="+url.QueryEscape(id), nil))
		if rec.Code != http.StatusOK {
			continue
		}
		name := fmt.Sprintf("doc-%d.json", len(docs))
		docs[id] = name
		if err := writeFile(filepath.Join(dir, "api", "docs", name), rec.Body.Bytes()); err != nil {
			return err
		}
	}
	if err := writeJSONFile(filepath.Join(dir, "api", "docs.json"), docs); err != nil {
		return err
	}

	// 5. Anexos do escopo (imagens, PDF, CSV...), no mesmo caminho do Vault.
	scopeDir := filepath.Join(vaultDir, filepath.FromSlash(strings.TrimSuffix(docScope, "/")))
	return filepath.WalkDir(scopeDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(strings.ToLower(p), ".md") {
			return err
		}
		rel, _ := filepath.Rel(vaultDir, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return writeFile(filepath.Join(dir, "vault", rel), b)
	})
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func writeFile(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

func writeJSONFile(p string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeFile(p, b)
}
