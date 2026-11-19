package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Vínculo automático worktree → savepoint/frente.
//
// O savepoint declara a worktree no próprio frontmatter; o app só lê.
// Campos aceitos:
//
//	branch: dev.x            # ou branches: [dev.x, feat/y] / lista em bloco
//	repo: acme-app           # opcional: restringe o branch a um repo (dir ou key)
//	worktree: "[[worktree-app/worktrees/x--app]]"     # ou worktrees: [...]
//	front: "[[worktree-app/docs/kanbans/x]]"          # opcional
//	related: [...]           # sem front, vale o 1º link para docs/kanbans/
//
// Texto do corpo nunca é usado: casar nome de branch em prosa dá falso positivo.

type savepointLink struct {
	Savepoint string
	Front     string
	sortKey   string
}

type branchEntry struct {
	repo string
	link savepointLink
}

type savepointIndex struct {
	byNote   map[string]savepointLink
	byBranch map[string][]branchEntry
}

func newer(a, b *savepointLink) *savepointLink {
	if a == nil || (b != nil && b.sortKey > a.sortKey) {
		return b
	}
	return a
}

// find devolve o savepoint mais recente que aponta para a worktree, pela nota
// ou pela branch (respeitando o filtro de repo).
func (ix *savepointIndex) find(w WorktreeInfo, note string) *savepointLink {
	if ix == nil {
		return nil
	}
	var best *savepointLink
	if l, ok := ix.byNote[note]; ok {
		best = newer(best, &l)
	}
	if w.Branch != "" {
		for _, e := range ix.byBranch[w.Branch] {
			if e.repo == "" || e.repo == w.RepoDir || e.repo == w.Repo {
				l := e.link
				best = newer(best, &l)
			}
		}
	}
	return best
}

// applySavepointLink preenche vínculos vazios e atualiza os automáticos; o que
// foi definido na tela (source manual) não é sobrescrito.
func applySavepointLink(c *Connection, l *savepointLink) {
	if l == nil {
		return
	}
	manual := c.Source == "manual"
	if l.Savepoint != "" && (c.LastSavepoint == "" || !manual) {
		c.LastSavepoint = l.Savepoint
	}
	if l.Front != "" && (c.Front == "" || !manual) {
		c.Front = l.Front
	}
	if c.Source == "" {
		c.Source = "auto"
	}
}

func loadSavepointLinks(cfg *Config) *savepointIndex {
	if cfg.Vault == nil || cfg.Vault.Savepoints == "" {
		return nil
	}
	entries, err := os.ReadDir(cfg.Vault.Savepoints)
	if err != nil {
		return nil
	}
	ix := &savepointIndex{byNote: map[string]savepointLink{}, byBranch: map[string][]branchEntry{}}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") || strings.HasPrefix(name, "_") {
			continue
		}
		fm := readFrontmatter(filepath.Join(cfg.Vault.Savepoints, name))
		if fm == nil {
			continue
		}
		branches := append(fm["branch"], fm["branches"]...)
		notes := append(fm["worktree"], fm["worktrees"]...)
		if len(branches) == 0 && len(notes) == 0 {
			continue
		}
		base := strings.TrimSuffix(name, ".md")
		link := savepointLink{
			Savepoint: "[[" + strings.TrimSuffix(cfg.Vault.SavepointsLink, "/") + "/" + base + "]]",
			Front:     frontFrom(fm),
			sortKey:   first(fm["date"]) + "\x00" + base,
		}
		for _, n := range notes {
			n = wikiTarget(n)
			n = n[strings.LastIndex(n, "/")+1:]
			if n == "" {
				continue
			}
			if old, ok := ix.byNote[n]; !ok || link.sortKey > old.sortKey {
				ix.byNote[n] = link
			}
		}
		repo := first(fm["repo"])
		for _, b := range branches {
			if b != "" {
				ix.byBranch[b] = append(ix.byBranch[b], branchEntry{repo: repo, link: link})
			}
		}
	}
	return ix
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// wikiTarget tira [[ ]], alias (|) e âncora (#) de um link do Obsidian.
func wikiTarget(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimSuffix(strings.TrimPrefix(v, "[["), "]]")
	v, _, _ = strings.Cut(v, "|")
	v, _, _ = strings.Cut(v, "#")
	return strings.TrimSuffix(strings.TrimSpace(v), ".md")
}

func frontFrom(fm map[string][]string) string {
	candidates := fm["front"]
	for _, r := range fm["related"] {
		if strings.Contains(r, "/docs/kanbans/") {
			candidates = append(candidates, r)
		}
	}
	for _, c := range candidates {
		if v, err := normalizeWiki(wikiTarget(c)); err == nil && v != "" {
			return v
		}
	}
	return ""
}

var fmKey = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*):\s*(.*)$`)

// readFrontmatter lê só o bloco --- inicial, com o subconjunto de YAML que os
// savepoints usam: escalar, lista [a, b] e lista em bloco "  - a". Mapas
// aninhados (taxonomy:) são ignorados.
func readFrontmatter(path string) map[string][]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseFrontmatter(f)
}

func parseFrontmatter(rd io.Reader) map[string][]string {
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return nil
	}
	out := map[string][]string{}
	key := ""
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "---" {
			return out
		}
		if m := fmKey.FindStringSubmatch(line); m != nil {
			key = m[1]
			v := strings.TrimSpace(m[2])
			switch {
			case v == "":
			case strings.HasPrefix(v, "["):
				for _, item := range splitFlow(strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")) {
					out[key] = append(out[key], item)
				}
			default:
				out[key] = append(out[key], unquote(v))
			}
			continue
		}
		t := strings.TrimSpace(line)
		if key != "" && strings.HasPrefix(t, "- ") && line != t {
			out[key] = append(out[key], unquote(strings.TrimSpace(t[2:])))
		}
	}
	return nil // sem fechamento: não é frontmatter
}

// splitFlow separa "a, [[b]], 'c'" sem quebrar dentro de [[...]] ou aspas.
func splitFlow(s string) []string {
	var out []string
	var cur strings.Builder
	depth, quote := 0, byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '[':
			depth++
		case c == ']':
			depth--
		case c == ',' && depth == 0:
			if v := unquote(strings.TrimSpace(cur.String())); v != "" {
				out = append(out, v)
			}
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	if v := unquote(strings.TrimSpace(cur.String())); v != "" {
		out = append(out, v)
	}
	return out
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}
