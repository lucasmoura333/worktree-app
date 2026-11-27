package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const generatedBy = "worktree-app/v1"

type Connection struct {
	Note          string `json:"note"`
	Front         string `json:"front"`
	LastSavepoint string `json:"lastSavepoint"`
	UpdatedAt     string `json:"updatedAt"`
	// Source diz quem preencheu os vínculos: "auto" (derivado dos savepoints)
	// ou "manual" (tela). Vínculo manual não é sobrescrito pelo automático.
	Source string `json:"source,omitempty"`
}

type vaultNote struct {
	WT     WorktreeInfo
	Status string
}

// Encoding both parts keeps note paths independent of branch names and separators.
// Human-readable feature prefix keeps notes useful in Obsidian's file explorer.
func noteName(wt WorktreeInfo) string {
	return safeNoteName(wt.Feature, wt.Repo)
}

var notePart = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

func safeNoteName(feature, repo string) string {
	// Both fields are encoded, rather than interpreted as filesystem paths.
	return encodeNotePart(feature) + "--" + encodeNotePart(repo)
}
func encodeNotePart(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "~%02x", c)
		}
	}
	// Prevent collisions between the pair separator and characters within each part.
	return strings.ReplaceAll(b.String(), "--", "~2d~2d")
}

func vaultDir(cfg *Config) (string, error) {
	if cfg.Vault == nil || cfg.Vault.Path == "" {
		return "", nil
	}
	root, err := filepath.EvalSymlinks(cfg.Vault.Path)
	if err != nil {
		return "", fmt.Errorf("Vault indisponível: %w", err)
	}
	dir := filepath.Join(root, filepath.FromSlash(vaultFolder))
	// Refuse redirects at every existing path component, including Windows junctions.
	for p := dir; !samePath(p, root); p = filepath.Dir(p) {
		if !within(root, p) || isLink(p) {
			return "", errors.New("pasta gerada não pode conter links ou junctions")
		}
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

func quoteYAML(s string) string { b, _ := json.Marshal(s); return string(b) }

func renderNote(n vaultNote) []byte {
	w := n.WT
	c := w.Connection
	var b strings.Builder
	b.WriteString("---\n")
	fields := [][2]string{{"generated_by", generatedBy}, {"type", "worktree"}, {"slug", w.Feature}, {"repo_key", w.Repo}, {"repo", w.RepoDir}, {"branch", w.Branch}, {"front", c.Front}, {"status", n.Status}, {"sha", w.Head}, {"upstream", w.Upstream}, {"docker_project", w.DockerProject}, {"last_savepoint", c.LastSavepoint}, {"link_source", c.Source}, {"updated_at", c.UpdatedAt}}
	for _, f := range fields {
		fmt.Fprintf(&b, "%s: %s\n", f[0], quoteYAML(f[1]))
	}
	for _, f := range []struct {
		k string
		v int
	}{{"ahead", w.Ahead}, {"behind", w.Behind}, {"dirty", w.Dirty}, {"unpushed", w.Unpushed}, {"port", w.Port}} {
		fmt.Fprintf(&b, "%s: %d\n", f.k, f.v)
	}
	b.WriteString("---\n")
	return []byte(b.String())
}

// Only read our own property-only notes. Never replace authored prose or foreign files.
func readNote(path string) (vaultNote, error) {
	var n vaultNote
	if isLink(path) {
		return n, errors.New("nota gerada não pode ser link")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return n, err
	}
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return n, errors.New("nota sem propriedades geradas")
	}
	parts := strings.SplitN(text[4:], "\n---", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[1]) != "" {
		return n, errors.New("nota contém conteúdo manual; mantida")
	}
	values := map[string]string{}
	for _, line := range strings.Split(parts[0], "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			return n, errors.New("propriedades inválidas")
		}
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, "\"") {
			var decoded string
			if err := json.Unmarshal([]byte(v), &decoded); err != nil {
				return n, err
			}
			v = decoded
		}
		values[k] = v
	}
	if values["generated_by"] != generatedBy || values["type"] != "worktree" {
		return n, errors.New("arquivo não pertence ao wt; mantido")
	}
	w := &n.WT
	w.Feature, w.Repo, w.RepoDir, w.Branch, w.Head, w.Upstream, w.DockerProject = values["slug"], values["repo_key"], values["repo"], values["branch"], values["sha"], values["upstream"], values["docker_project"]
	w.Ahead, _ = strconv.Atoi(values["ahead"])
	w.Behind, _ = strconv.Atoi(values["behind"])
	w.Dirty, _ = strconv.Atoi(values["dirty"])
	w.Unpushed, _ = strconv.Atoi(values["unpushed"])
	w.Port, _ = strconv.Atoi(values["port"])
	w.Connection = &Connection{Note: vaultFolder + "/" + noteName(*w), Front: values["front"], LastSavepoint: values["last_savepoint"], UpdatedAt: values["updated_at"], Source: values["link_source"]}
	if n.WT.Connection.Source == "" && (n.WT.Connection.Front != "" || n.WT.Connection.LastSavepoint != "") {
		// Notas anteriores ao link_source só tinham vínculo vindo da tela.
		n.WT.Connection.Source = "manual"
	}
	n.Status = values["status"]
	return n, nil
}

func writeNote(dir string, n vaultNote) error {
	name := noteName(n.WT)
	if name == "--" || len(name) > 220 {
		return errors.New("nome da worktree longo demais para nota")
	}
	dest := filepath.Join(dir, name+".md")
	if exists(dest) {
		if _, err := readNote(dest); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return writeFileIfChanged(dir, dest, renderNote(n), ".wt-*.tmp")
}

// writeFileIfChanged grava de forma atômica e não toca no arquivo quando o
// conteúdo é o mesmo (evita commit vazio do obsidian-git a cada atualização).
func writeFileIfChanged(dir, dest string, content []byte, pattern string) error {
	if old, err := os.ReadFile(dest); err == nil && strings.ReplaceAll(string(old), "\r\n", "\n") == string(content) {
		return nil
	}
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return err
	}
	tempName := tmp.Name()
	defer os.Remove(tempName)
	if _, err = tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, dest)
}

// syncVault grava as notas e as visões a partir do estado.
func (s *server) syncVault(st *State) { s.syncVaultMode(st, true) }

// peekVault só lê as notas para mostrar vínculos na tela, sem gravar nada.
// Usado quando uma operação está em andamento e o estado é intermediário.
func (s *server) peekVault(st *State) { s.syncVaultMode(st, false) }

func (s *server) syncVaultMode(st *State, write bool) {
	s.vaultMu.Lock()
	defer s.vaultMu.Unlock()
	st.Archived = []WorktreeInfo{}
	if s.cfg.Vault == nil || s.cfg.Vault.Path == "" {
		return
	}
	st.Vault = filepath.Base(filepath.Clean(s.cfg.Vault.Path))
	if err := s.syncVaultLocked(st, write); err != nil {
		st.VaultError = err.Error()
	}
}

// nestedInMain diz se a worktree está dentro do repo principal (ex.: .bench/):
// é ferramenta, não tarefa, e fica fora do Vault.
func (s *server) nestedInMain(w WorktreeInfo) bool {
	r := s.cfg.repo(w.Repo)
	if r == nil {
		return false
	}
	main := s.cfg.mainPath(r)
	return !samePath(main, w.Path) && within(main, w.Path)
}

// identityKey reconhece a mesma worktree após mudar o nome da pasta: pela
// branch, ou pelo SHA quando ela está em detached.
func identityKey(w WorktreeInfo) string {
	if w.Branch != "" {
		return w.Repo + "\x00" + w.Branch
	}
	if w.Head != "" {
		return w.Repo + "\x00@" + w.Head
	}
	return ""
}

func (s *server) syncVaultLocked(st *State, write bool) error {
	dir, err := vaultDir(s.cfg)
	if err != nil {
		return err
	}
	notes := map[string]vaultNote{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		n, err := readNote(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		} // Foreign files are ignored; a collision is rejected by writeNote.
		if e.Name() != noteName(n.WT)+".md" {
			continue
		}
		notes[noteName(n.WT)] = n
	}
	seen := map[string]bool{}
	for _, wt := range st.Stale {
		seen[noteName(wt)] = true
	}
	now := time.Now().Format(time.RFC3339)
	var errs []error
	activeNames := map[string]int{}
	ignored := map[string]bool{}
	for _, f := range st.Features {
		for _, w := range f.Worktrees {
			activeNames[noteName(w)]++
		}
	}
	// Notas ativas por repo+branch: quando a pasta muda de nome (ex.: novo
	// folderPattern), a nota nova herda os vínculos e a antiga sai.
	byBranch := map[string]string{}
	for name, n := range notes {
		if k := identityKey(n.WT); n.Status == "ativo" && k != "" && activeNames[name] == 0 {
			byBranch[k] = name
		}
	}
	renamed := map[string]bool{}
	links := loadSavepointLinks(s.cfg)
	for fi := range st.Features {
		f := &st.Features[fi]
		for wi := range f.Worktrees {
			w := &f.Worktrees[wi]
			name := noteName(*w)
			seen[name] = true
			if s.nestedInMain(*w) {
				ignored[name] = true
				continue
			}
			if activeNames[name] > 1 {
				errs = append(errs, fmt.Errorf("mais de uma worktree usa a identidade %s; nota mantida", name))
				continue
			}
			previous, hasPrev := notes[name]
			if k := identityKey(*w); !hasPrev && k != "" {
				if old, ok := byBranch[k]; ok {
					previous, hasPrev = notes[old], true
					renamed[old] = true
					delete(byBranch, k)
				}
			}
			c := &Connection{Note: vaultFolder + "/" + name, UpdatedAt: now}
			if hasPrev {
				pc := previous.WT.Connection
				c.Front, c.LastSavepoint, c.Source, c.UpdatedAt = pc.Front, pc.LastSavepoint, pc.Source, pc.UpdatedAt
			}
			applySavepointLink(c, links.find(*w, name))
			w.Connection = c
			if w.Error != "" {
				if hasPrev {
					w.Connection = previous.WT.Connection
				}
				continue
			}
			if !write {
				continue
			}
			n := vaultNote{WT: *w, Status: "ativo"}
			// updated_at só muda quando o resto da nota muda.
			if hasPrev && c.UpdatedAt != "" && notes[name].WT.Connection != nil && string(renderNote(n)) == string(renderNote(notes[name])) {
				continue
			}
			c.UpdatedAt = now
			if err := writeNote(dir, n); err != nil {
				errs = append(errs, err)
			}
		}
	}
	healthy := map[string]bool{}
	for _, r := range st.Repos {
		healthy[r.Key] = r.Error == ""
	}
	for name, n := range notes {
		if renamed[name] || ignored[name] {
			// Nota gerada por nós que perdeu o sentido: renomeada ou de worktree
			// de ferramenta. Sai em vez de virar "removido".
			if write {
				if err := os.Remove(filepath.Join(dir, name+".md")); err != nil && !os.IsNotExist(err) {
					errs = append(errs, err)
				}
			}
			continue
		}
		if seen[name] {
			continue
		}
		if !healthy[n.WT.Repo] && n.Status != "removido" {
			continue
		}
		if n.Status != "removido" {
			if !write {
				continue
			}
			n.Status = "removido"
			n.WT.Connection.UpdatedAt = now
			if err := writeNote(dir, n); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		st.Archived = append(st.Archived, n.WT)
	}
	sort.Slice(st.Archived, func(i, j int) bool { return st.Archived[i].Feature < st.Archived[j].Feature })
	if write && len(errs) == 0 {
		if err := writeVaultViews(dir, st); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func normalizeWiki(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	value = strings.TrimSuffix(strings.TrimPrefix(value, "[["), "]]")
	if strings.ContainsAny(value, "\r\n[]|\\") || !strings.HasPrefix(value, docScope) || strings.Contains(value, "..") || strings.Contains(value, ":") {
		return "", fmt.Errorf("use um caminho de nota dentro de %s", docScope)
	}
	return "[[" + strings.TrimSuffix(value, ".md") + "]]", nil
}

func (s *server) handleConnections(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Repo          string `json:"repo"`
		Feature       string `json:"feature"`
		Front         string `json:"front"`
		LastSavepoint string `json:"lastSavepoint"`
	}
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	front, err := normalizeWiki(req.Front)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	savepoint, err := normalizeWiki(req.LastSavepoint)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if s.cfg.repo(req.Repo) == nil || !notePart.MatchString(req.Feature) {
		http.Error(w, "worktree inválida", 400)
		return
	}
	release, ok := s.tryOp()
	if !ok {
		http.Error(w, "operação em andamento", 409)
		return
	}
	defer release()
	s.vaultMu.Lock()
	defer s.vaultMu.Unlock()
	dir, err := vaultDir(s.cfg)
	if err != nil || dir == "" {
		http.Error(w, "Vault indisponível", 400)
		return
	}
	n, err := readNote(filepath.Join(dir, safeNoteName(req.Feature, req.Repo)+".md"))
	if err != nil {
		http.Error(w, "nota gerada não encontrada; atualize o estado", 404)
		return
	}
	n.WT.Connection.Front, n.WT.Connection.LastSavepoint, n.WT.Connection.UpdatedAt = front, savepoint, time.Now().Format(time.RFC3339)
	// Limpar os dois campos devolve o vínculo ao automático (savepoints).
	n.WT.Connection.Source = "manual"
	if front == "" && savepoint == "" {
		n.WT.Connection.Source = ""
	}
	if err := writeNote(dir, n); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
