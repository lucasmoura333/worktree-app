package main

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Grafo local das notas do escopo do Vault (GET /api/graph), análogo ao do
// Obsidian: nós são notas de docScope, arestas são [[wikilinks]] (e vínculos
// declarados no frontmatter dos savepoints) e, à parte, menções de branch no
// texto dos savepoints. Só leitura e só dentro de docScope.

type graphNote struct {
	ID, Label, Kind, Title string
	out                    map[string]bool
}

type linkIndex struct {
	built    time.Time
	notes    map[string]*graphNote
	in       map[string]map[string]bool // alvo -> quem linka
	mentions map[string]map[string]bool // nos dois sentidos: savepoint <-> worktree
}

type graphCache struct {
	mu       sync.Mutex
	idx      *linkIndex
	building bool
}

const graphTTL = time.Minute

var wikiRe = regexp.MustCompile(`!?\[\[([^\]\n]+)\]\]`)

func noteKind(id string) string {
	switch {
	case strings.HasPrefix(id, vaultFolder+"/"):
		return "worktree"
	case strings.Contains(id, "/docs/kanbans/"):
		return "kanban"
	case strings.Contains(id, "/docs/savepoints/"):
		return "savepoint"
	case strings.Contains(id, "/Diario/"):
		return "diario"
	}
	return "doc"
}

// graphIndex devolve o índice; vencido, é refeito em segundo plano e quem
// pediu recebe o anterior (a primeira montagem leva uns segundos no Vault).
func (s *server) graphIndex(force bool) *linkIndex {
	g := &s.graph
	g.mu.Lock()
	idx := g.idx
	stale := idx == nil || force || time.Since(idx.built) > graphTTL
	if !stale || (idx != nil && g.building) {
		g.mu.Unlock()
		return idx
	}
	if idx != nil && !force {
		g.building = true
		g.mu.Unlock()
		go func() {
			fresh := s.buildLinkIndex()
			g.mu.Lock()
			g.idx, g.building = fresh, false
			g.mu.Unlock()
		}()
		return idx
	}
	g.mu.Unlock()
	fresh := s.buildLinkIndex()
	g.mu.Lock()
	g.idx = fresh
	g.mu.Unlock()
	return fresh
}

func (s *server) buildLinkIndex() *linkIndex {
	v := s.cfg.Vault
	files := map[string]string{} // id -> arquivo
	byBase := map[string]string{}
	add := func(dir, prefix string, skipDocs bool) {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if p != dir && (docSkipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
					return filepath.SkipDir
				}
				// O docs/ do Vault é espelho do canônico, que já entrou.
				if skipDocs && p == filepath.Join(dir, "docs") {
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
			id := prefix + "/" + strings.TrimSuffix(filepath.ToSlash(rel), ".md")
			if _, dup := files[id]; dup {
				return nil
			}
			files[id] = p
			key := strings.ToLower(strings.TrimSuffix(d.Name(), ".md"))
			if _, dup := byBase[key]; !dup {
				byBase[key] = id
			}
			return nil
		})
	}
	canonical := v.Docs != "" && strings.TrimSuffix(v.DocsLink, "/") == strings.TrimSuffix(docScope, "/")+"/docs"
	if v.Docs != "" {
		add(v.Docs, strings.TrimSuffix(v.DocsLink, "/"), false)
	}
	add(filepath.Join(v.Path, filepath.FromSlash(strings.TrimSuffix(docScope, "/"))), strings.TrimSuffix(docScope, "/"), canonical)

	idx := &linkIndex{built: time.Now(), notes: map[string]*graphNote{}, in: map[string]map[string]bool{}, mentions: map[string]map[string]bool{}}
	type wtInfo struct{ id, branch, name string }
	var wts []wtInfo
	texts := map[string]string{} // só savepoints, para as menções
	for id, p := range files {
		info, err := os.Stat(p)
		if err != nil || info.Size() > docMaxBytes {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		text := string(b)
		fm := parseFrontmatter(strings.NewReader(text))
		base := id[strings.LastIndex(id, "/")+1:]
		n := &graphNote{ID: id, Label: base, Kind: noteKind(id), Title: unquote(first(fm["title"])), out: map[string]bool{}}
		if n.Kind == "worktree" {
			n.Label = decodeNotePart(base) // acme-api~2eexpansion -> .expansion
		}
		for _, m := range wikiRe.FindAllStringSubmatch(text, -1) {
			t, err := cleanDocLink(m[1])
			if err != nil {
				continue
			}
			if !strings.Contains(t, "/") {
				t = byBase[strings.ToLower(t)]
			}
			if _, ok := files[t]; ok && t != id {
				n.out[t] = true
			}
		}
		idx.notes[id] = n
		switch n.Kind {
		case "worktree":
			wts = append(wts, wtInfo{id, first(fm["branch"]), base})
		case "savepoint":
			texts[id] = text
		}
	}
	for id, text := range texts {
		fm := parseFrontmatter(strings.NewReader(text))
		declared := map[string]bool{}
		for _, x := range append(fm["branch"], fm["branches"]...) {
			declared["b:"+x] = true
		}
		for _, x := range append(fm["worktree"], fm["worktrees"]...) {
			t := wikiTarget(x)
			declared["n:"+t[strings.LastIndex(t, "/")+1:]] = true
		}
		for _, w := range wts {
			switch {
			case (w.branch != "" && declared["b:"+w.branch]) || declared["n:"+w.name]:
				idx.notes[id].out[w.id] = true
			case (len(w.branch) >= 6 && containsToken(text, w.branch)) || containsToken(text, w.name):
				link(idx.mentions, id, w.id)
				link(idx.mentions, w.id, id)
			}
		}
	}
	for id, n := range idx.notes {
		for t := range n.out {
			link(idx.in, t, id)
		}
	}
	return idx
}

var noteEsc = regexp.MustCompile(`~([0-9a-f]{2})`)

// decodeNotePart desfaz o encodeNotePart só para exibição.
func decodeNotePart(s string) string {
	return noteEsc.ReplaceAllStringFunc(s, func(m string) string {
		v, err := strconv.ParseUint(m[1:], 16, 8)
		if err != nil {
			return m
		}
		return string(rune(v))
	})
}

func link(m map[string]map[string]bool, a, b string) {
	if m[a] == nil {
		m[a] = map[string]bool{}
	}
	m[a][b] = true
}

type graphNode struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	Title string `json:"title,omitempty"`
	Level int    `json:"level"`
}

type graphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"` // link | mention
}

type graphResp struct {
	Nodes     []graphNode `json:"nodes"`
	Edges     []graphEdge `json:"edges"`
	Truncated bool        `json:"truncated,omitempty"`
	Missing   []string    `json:"missing,omitempty"`
	Built     string      `json:"built"`
}

const (
	graphMaxNodes  = 160
	graphPerSeed   = 60 // vizinhos de cada nó de partida
	graphPerDeeper = 12 // vizinhos de cada nó do nível 1 (profundidade 2)
)

var kindRank = map[string]int{"worktree": 0, "kanban": 1, "savepoint": 2, "doc": 3, "diario": 4}

func (s *server) handleGraph(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Vault == nil || s.cfg.Vault.Path == "" {
		http.Error(w, "Vault não configurado", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	depth, _ := strconv.Atoi(q.Get("depth"))
	if depth != 2 {
		depth = 1
	}
	withMentions, withDiario := q.Get("mentions") != "0", q.Get("diario") == "1"
	idx := s.graphIndex(q.Get("refresh") == "1")
	resp := graphResp{Nodes: []graphNode{}, Edges: []graphEdge{}, Built: idx.built.Format(time.RFC3339)}

	level := map[string]int{}
	var order, frontier []string
	for _, raw := range q["seed"] {
		id, err := cleanDocLink(raw)
		if err == nil && !strings.Contains(id, "/") {
			id = idx.byName(id)
		}
		if _, ok := idx.notes[id]; err != nil || !ok || !strings.HasPrefix(id, docScope) {
			resp.Missing = append(resp.Missing, raw)
			continue
		}
		if _, seen := level[id]; !seen {
			level[id] = 0
			order = append(order, id)
			frontier = append(frontier, id)
		}
	}
	allowed := func(id string) bool {
		n := idx.notes[id]
		if n == nil || (!withDiario && n.Kind == "diario") {
			return false
		}
		// Índices (_INDEX) linkam centenas de notas: só entram como partida.
		return !strings.HasPrefix(n.Label, "_")
	}
	for d := 1; d <= depth && len(frontier) > 0; d++ {
		limit := graphPerSeed
		if d > 1 {
			limit = graphPerDeeper
		}
		var next []string
		for _, id := range frontier {
			for i, nb := range idx.neighbors(id, withMentions) {
				if i >= limit {
					resp.Truncated = true
					break
				}
				if _, seen := level[nb]; seen || !allowed(nb) {
					continue
				}
				if len(order) >= graphMaxNodes {
					resp.Truncated = true
					break
				}
				level[nb] = d
				order = append(order, nb)
				next = append(next, nb)
			}
		}
		frontier = next
	}
	for _, id := range order {
		n := idx.notes[id]
		resp.Nodes = append(resp.Nodes, graphNode{ID: id, Label: n.Label, Kind: n.Kind, Title: n.Title, Level: level[id]})
	}
	// Arestas entre quaisquer nós presentes (não só as da busca).
	for _, a := range order {
		for b := range idx.notes[a].out {
			if _, ok := level[b]; ok {
				resp.Edges = append(resp.Edges, graphEdge{a, b, "link"})
			}
		}
		if withMentions {
			for b := range idx.mentions[a] {
				if _, ok := level[b]; ok && a < b && !idx.notes[a].out[b] && !idx.notes[b].out[a] {
					resp.Edges = append(resp.Edges, graphEdge{a, b, "mention"})
				}
			}
		}
	}
	sort.Slice(resp.Edges, func(i, j int) bool {
		if resp.Edges[i].From != resp.Edges[j].From {
			return resp.Edges[i].From < resp.Edges[j].From
		}
		return resp.Edges[i].To < resp.Edges[j].To
	})
	writeJSON(w, resp)
}

func (ix *linkIndex) byName(name string) string {
	name = strings.ToLower(name)
	best := ""
	for id, n := range ix.notes {
		if strings.ToLower(n.Label) == name && (best == "" || id < best) {
			best = id
		}
	}
	return best
}

// neighbors: links de saída, backlinks e (opcional) menções, em ordem estável:
// worktree, kanban, savepoint (mais novo primeiro), doc, diário.
func (ix *linkIndex) neighbors(id string, withMentions bool) []string {
	set := map[string]bool{}
	for t := range ix.notes[id].out {
		set[t] = true
	}
	for t := range ix.in[id] {
		set[t] = true
	}
	if withMentions {
		for t := range ix.mentions[id] {
			set[t] = true
		}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := ix.notes[out[i]], ix.notes[out[j]]
		if kindRank[a.Kind] != kindRank[b.Kind] {
			return kindRank[a.Kind] < kindRank[b.Kind]
		}
		return a.ID > b.ID
	})
	return out
}
