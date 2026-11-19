package main

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Leitor de notas: GET /api/doc?path=<wikilink> devolve o markdown de uma nota
// do escopo do Vault (vault.scope) para o painel renderizar. Só leitura e só
// dentro de docScope: notas pessoais fora do escopo nunca passam por aqui.

// defaultVaultScope é a pasta do Vault usada quando vault.scope não é definido.
const defaultVaultScope = "worktree-app"

// docScope ("<scope>/") e vaultFolder ("<scope>/worktrees") valem para o
// processo inteiro: cada execução lê um único config (ver setVaultScope).
var (
	docScope    = defaultVaultScope + "/"
	vaultFolder = defaultVaultScope + "/worktrees"
)

// setVaultScope fixa a pasta do Vault que o app pode ler e escrever.
func setVaultScope(scope string) {
	scope = cleanVaultScope(scope)
	if scope == "" {
		scope = defaultVaultScope
	}
	docScope, vaultFolder = scope+"/", scope+"/worktrees"
}

func cleanVaultScope(scope string) string {
	return strings.Trim(strings.ReplaceAll(strings.TrimSpace(scope), "\\", "/"), "/")
}

const docMaxBytes = 2 << 20

type docIndex struct {
	mu     sync.Mutex
	built  time.Time
	byBase map[string]string // nome do arquivo (minúsculo, sem .md) -> caminho no Vault
}

var errDocScope = errors.New("nota fora do escopo do Vault (vault.scope)")

// docCandidate é um arquivo possível para a nota e a raiz que ele não pode deixar.
type docCandidate struct {
	file, root, source string
}

func (s *server) docCandidates(rel string) []docCandidate {
	v := s.cfg.Vault
	var out []docCandidate
	// vault.docs (fora do Vault) vem antes do espelho: costuma estar mais novo.
	if v.Docs != "" {
		prefix := strings.TrimSuffix(v.DocsLink, "/") + "/"
		if strings.HasPrefix(rel, prefix) {
			out = append(out, docCandidate{filepath.Join(v.Docs, filepath.FromSlash(strings.TrimPrefix(rel, prefix))) + ".md", v.Docs, "docs"})
		}
	}
	gd := filepath.Join(v.Path, filepath.FromSlash(strings.TrimSuffix(docScope, "/")))
	out = append(out, docCandidate{filepath.Join(v.Path, filepath.FromSlash(rel)) + ".md", gd, "vault"})
	return out
}

// cleanDocLink valida o alvo de um wikilink e devolve o caminho sem .md.
func cleanDocLink(link string) (string, error) {
	t := wikiTarget(link)
	if t == "" || strings.ContainsAny(t, ":\\\x00") || strings.HasPrefix(t, "/") || strings.HasPrefix(t, ".") {
		return "", errors.New("link inválido")
	}
	for _, part := range strings.Split(t, "/") {
		if part == ".." || part == "." || part == "" {
			return "", errors.New("link inválido")
		}
	}
	return t, nil
}

func (s *server) resolveDoc(link string) (rel string, c docCandidate, err error) {
	if s.cfg.Vault == nil || s.cfg.Vault.Path == "" {
		return "", c, errors.New("Vault não configurado")
	}
	rel, err = cleanDocLink(link)
	if err != nil {
		return "", c, err
	}
	if !strings.Contains(rel, "/") {
		// [[11-10-2026]]: o Obsidian resolve pelo nome do arquivo.
		found, ok := s.lookupDocName(rel)
		if !ok {
			return "", c, os.ErrNotExist
		}
		rel = found
	}
	if !strings.HasPrefix(rel, docScope) {
		return "", c, errDocScope
	}
	for _, cand := range s.docCandidates(rel) {
		if safeDocFile(cand) {
			return rel, cand, nil
		}
	}
	return "", c, os.ErrNotExist
}

// safeDocFile confere, já resolvendo links e junctions, que o arquivo é
// regular, pequeno e continua dentro da raiz.
func safeDocFile(c docCandidate) bool {
	root, err := filepath.EvalSymlinks(c.root)
	if err != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(c.file)
	if err != nil || !within(root, real) {
		return false
	}
	info, err := os.Stat(real)
	return err == nil && info.Mode().IsRegular() && info.Size() <= docMaxBytes
}

var docSkipDirs = map[string]bool{".git": true, ".obsidian": true, ".trash": true, "node_modules": true, "assets": true}

func (s *server) lookupDocName(name string) (string, bool) {
	ix := &s.docs
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.byBase == nil || time.Since(ix.built) > 2*time.Minute {
		ix.byBase = map[string]string{}
		v := s.cfg.Vault
		// O canônico entra primeiro: em nome repetido, vale ele.
		if v.Docs != "" {
			indexDocs(ix.byBase, v.Docs, strings.TrimSuffix(v.DocsLink, "/"))
		}
		indexDocs(ix.byBase, filepath.Join(v.Path, filepath.FromSlash(strings.TrimSuffix(docScope, "/"))), strings.TrimSuffix(docScope, "/"))
		ix.built = time.Now()
	}
	rel, ok := ix.byBase[strings.ToLower(name)]
	return rel, ok
}

func indexDocs(into map[string]string, dir, prefix string) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && (docSkipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return nil
		}
		key := strings.ToLower(strings.TrimSuffix(d.Name(), ".md"))
		if _, dup := into[key]; !dup {
			into[key] = prefix + "/" + strings.TrimSuffix(filepath.ToSlash(rel), ".md")
		}
		return nil
	})
}

func (s *server) handleDoc(w http.ResponseWriter, r *http.Request) {
	rel, c, err := s.resolveDoc(r.URL.Query().Get("path"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		http.Error(w, "nota não encontrada", http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	b, err := os.ReadFile(c.file)
	if err != nil {
		http.Error(w, "nota não encontrada", http.StatusNotFound)
		return
	}
	info, _ := os.Stat(c.file)
	resp := docResponse{Path: rel, Text: string(b), Source: c.source, Assets: s.docAssets(rel, string(b))}
	if info != nil {
		resp.Modified = info.ModTime().Format(time.RFC3339)
	}
	if strings.HasPrefix(rel, vaultFolder+"/") {
		if n, err := readNote(c.file); err == nil {
			resp.Mentions = s.mentions(n.WT.Branch, noteName(n.WT))
		}
	}
	writeJSON(w, resp)
}

type docResponse struct {
	Path     string               `json:"path"`
	Text     string               `json:"text"`
	Source   string               `json:"source"`
	Modified string               `json:"modified"`
	Mentions []mention            `json:"mentions,omitempty"`
	Assets   map[string]assetInfo `json:"assets,omitempty"`
}

// mention é um savepoint que fala da worktree. Linked: declara a branch ou a
// nota no frontmatter (é o que vincula). Senão é só menção no texto, mostrada
// como contexto e nunca usada para vincular.
type mention struct {
	Path   string `json:"path"`
	Title  string `json:"title"`
	Date   string `json:"date"`
	Linked bool   `json:"linked"`
}

const maxMentions = 15

func (s *server) mentions(branch, note string) []mention {
	v := s.cfg.Vault
	if v == nil || v.Savepoints == "" {
		return nil
	}
	entries, err := os.ReadDir(v.Savepoints)
	if err != nil {
		return nil
	}
	// Branch curta demais ("dev", "main") casaria com tudo.
	useBranch := len(branch) >= 6 && branch != "origin/dev"
	var out []mention
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") || strings.HasPrefix(name, "_") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(v.Savepoints, name))
		if err != nil || len(b) > docMaxBytes {
			continue
		}
		text := string(b)
		fm := parseFrontmatter(strings.NewReader(text))
		linked := false
		for _, x := range append(fm["branch"], fm["branches"]...) {
			linked = linked || (branch != "" && x == branch)
		}
		for _, x := range append(fm["worktree"], fm["worktrees"]...) {
			t := wikiTarget(x)
			linked = linked || t[strings.LastIndex(t, "/")+1:] == note
		}
		if !linked && !(useBranch && containsToken(text, branch)) && !containsToken(text, note) {
			continue
		}
		base := strings.TrimSuffix(name, ".md")
		m := mention{Path: strings.TrimSuffix(v.SavepointsLink, "/") + "/" + base, Title: unquote(first(fm["title"])), Date: first(fm["date"]), Linked: linked}
		if m.Title == "" {
			m.Title = base
		}
		if m.Date == "" && len(base) >= 10 {
			m.Date = base[:10]
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Linked != out[j].Linked {
			return out[i].Linked
		}
		if out[i].Date != out[j].Date {
			return out[i].Date > out[j].Date
		}
		return out[i].Path > out[j].Path
	})
	if len(out) > maxMentions {
		out = out[:maxMentions]
	}
	return out
}

// containsToken acha s em text sem casar pedaço de outro nome
// (dev.x não casa em dev.x-2 nem em feat/dev.x).
func containsToken(text, s string) bool {
	if s == "" {
		return false
	}
	isName := func(c byte) bool {
		return c == '.' || c == '_' || c == '-' || c == '/' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
	}
	for i := 0; ; {
		k := strings.Index(text[i:], s)
		if k < 0 {
			return false
		}
		start, end := i+k, i+k+len(s)
		before := start == 0 || !isName(text[start-1])
		// Ponto final de frase depois do nome não conta como parte dele.
		after := end == len(text) || !isName(text[end]) || (text[end] == '.' && (end+1 == len(text) || !isName(text[end+1])))
		if before && after {
			return true
		}
		i = start + 1
	}
}
