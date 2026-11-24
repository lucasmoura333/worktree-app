package main

// Snapshots: um retrato local de uma worktree antes de algo arriscado (ou por
// pedido). Grava patch (HEAD → árvore de trabalho), untracked em tar e um
// bundle quando há commits fora do remoto, sempre sem segredos.
//
// O snapshot vive em stateDir/snapshots/<repo>/<pasta>/<data-hora>/ e nunca
// toca o repositório nem o Vault. Restaurar exige worktree limpa: primeiro
// valida patch e tar, depois aplica.

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const snapshotTimeLayout = "20060102-150405"

// snapshotNow é trocado nos testes para controlar a retenção.
var snapshotNow = time.Now

type SnapshotAgent struct {
	Agent   string `json:"agent"`
	Session string `json:"session"`
	State   string `json:"state"`
}

type SnapshotMeta struct {
	Repo          string          `json:"repo"`
	Folder        string          `json:"folder"`
	Path          string          `json:"path"`
	Branch        string          `json:"branch"`
	Head          string          `json:"head"`
	Upstream      string          `json:"upstream,omitempty"`
	Base          string          `json:"base,omitempty"`
	MergeBase     string          `json:"mergeBase,omitempty"`
	Dirty         int             `json:"dirty"`
	Unpushed      int             `json:"unpushed"`
	Omitted       []string        `json:"omitted"`
	Agents        []SnapshotAgent `json:"agents"`
	LastSavepoint string          `json:"lastSavepoint,omitempty"`
	WtNote        string          `json:"wtNote,omitempty"`
	Note          string          `json:"note,omitempty"`
	Reason        string          `json:"reason"`
	Bytes         int64           `json:"bytes"`
	CreatedAt     time.Time       `json:"createdAt"`
	HasPatch      bool            `json:"hasPatch,omitempty"`
	HasUntracked  bool            `json:"hasUntracked,omitempty"`
	HasBundle     bool            `json:"hasBundle,omitempty"`
	// Dir só é preenchido na listagem (caminho absoluto da pasta).
	Dir string `json:"dir,omitempty"`
}

func snapshotCfg(c *Config) SnapshotConfig {
	if c.Snapshots == nil {
		return defaultSnapshotConfig()
	}
	return *c.Snapshots
}

func stateRoot(c *Config) string {
	if c.StateDir != "" {
		return c.StateDir
	}
	return defaultStateDir()
}

func snapshotsRoot(c *Config) string { return filepath.Join(stateRoot(c), "snapshots") }

func safeSnapshotSeg(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" || out == "." || out == ".." {
		return "_"
	}
	return out
}

func snapshotRunDir(c *Config, repo, folder string) string {
	return filepath.Join(snapshotsRoot(c), safeSnapshotSeg(repo), safeSnapshotSeg(folder))
}

// snapshotHidden decide o que nunca entra no snapshot: o copy do repo, as
// deps e a lista fixa de nomes sensíveis. Só o nome vai para meta/log.
func snapshotHidden(r *RepoConfig, name string) bool {
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	if name == "" {
		return true
	}
	if excludedName(r, name) {
		return true
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".claude" || seg == "secrets" {
			return true
		}
	}
	base := path.Base(name)
	if strings.HasPrefix(base, ".env") && !strings.HasSuffix(base, ".example") {
		return true
	}
	if strings.HasPrefix(base, "id_rsa") || base == ".npmrc" {
		return true
	}
	for _, pat := range []string{"*.pem", "*.key", "*.p12", "*.pfx", "credentials*.json"} {
		if ok, _ := path.Match(pat, base); ok {
			return true
		}
	}
	return false
}

type snapshotFile struct {
	Name string
	Size int64
	Mode os.FileMode
}

func appendOmitted(list []string, seen map[string]bool, name string) []string {
	if seen[name] {
		return list
	}
	seen[name] = true
	return append(list, name)
}

// splitTracked separa os caminhos alterados desde HEAD entre o que entra e o
// que é omitido por segredo/dep/copy.
func splitTracked(r *RepoConfig, names []string) (keep, omitted []string) {
	seen := map[string]bool{}
	for _, n := range names {
		if n == "" {
			continue
		}
		if snapshotHidden(r, n) {
			omitted = appendOmitted(omitted, seen, n)
		} else {
			keep = append(keep, n)
		}
	}
	return keep, omitted
}

func gitNameZ(ctx context.Context, dir string, args ...string) ([]string, error) {
	out, cut, err := gitRO(ctx, dir, 8<<20, args...)
	if err != nil {
		return nil, err
	}
	if cut {
		return nil, errors.New("lista de arquivos grande demais para o snapshot")
	}
	var names []string
	for _, n := range strings.Split(out, "\x00") {
		if n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}

func maxBytesFor(limit int64) int {
	maxInt := int64(int(^uint(0) >> 1))
	if limit > maxInt {
		return int(maxInt)
	}
	return int(limit)
}

// createSnapshot grava o snapshot e aplica a retenção. reason é "manual" ou
// "pre-remove". O caller já validou a worktree.
func createSnapshot(ctx context.Context, cfg *Config, r *RepoConfig, wt WorktreeInfo, reason, note string, lg Logger) (SnapshotMeta, error) {
	var meta SnapshotMeta
	if r == nil {
		return meta, errors.New("snapshot: repo desconhecido")
	}
	if reason != "pre-remove" {
		reason = "manual"
	}
	sc := snapshotCfg(cfg)
	limit := int64(sc.MaxMB) << 20
	fileLimit := int64(sc.FileMaxMB) << 20
	folder := wt.Folder
	if folder == "" {
		folder = filepath.Base(wt.Path)
	}

	tracked, err := gitNameZ(ctx, wt.Path, "diff", "--name-only", "-z", "-M", "HEAD", "--", ".")
	if err != nil {
		return meta, err
	}
	keep, omitted := splitTracked(r, tracked)
	omittedSeen := map[string]bool{}
	for _, n := range omitted {
		omittedSeen[n] = true
	}
	// O git não lista o que está ignorado, então copy/deps e segredos de topo
	// precisam ser reconhecidos à parte para aparecerem em omitted.
	var ignored []string
	for _, c := range r.Copy {
		ignored = append(ignored, c)
	}
	for _, d := range r.Deps {
		ignored = append(ignored, d.Dir)
	}
	for _, n := range ignored {
		n = strings.TrimSuffix(filepath.ToSlash(n), "/")
		if n != "" && exists(filepath.Join(wt.Path, filepath.FromSlash(n))) {
			omitted = appendOmitted(omitted, omittedSeen, n)
		}
	}
	if ents, err := os.ReadDir(wt.Path); err == nil {
		for _, e := range ents {
			if snapshotHidden(r, e.Name()) {
				omitted = appendOmitted(omitted, omittedSeen, e.Name())
			}
		}
	}
	untrackedNames, err := gitNameZ(ctx, wt.Path, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return meta, err
	}
	var files []snapshotFile
	var total int64
	for _, n := range untrackedNames {
		if snapshotHidden(r, n) {
			omitted = appendOmitted(omitted, omittedSeen, n)
			continue
		}
		full := filepath.Join(wt.Path, filepath.FromSlash(n))
		fi, err := os.Lstat(full)
		if err != nil {
			continue
		}
		if !fi.Mode().IsRegular() || fi.Size() > fileLimit {
			omitted = appendOmitted(omitted, omittedSeen, n)
			continue
		}
		files = append(files, snapshotFile{Name: n, Size: fi.Size(), Mode: fi.Mode().Perm()})
		total += fi.Size()
		if total > limit {
			return meta, fmt.Errorf("snapshot passaria de maxMB=%d; nada foi gravado", sc.MaxMB)
		}
	}

	var patch string
	if len(keep) > 0 {
		args := []string{"diff", "--binary", "--no-ext-diff", "--no-textconv", "--no-color", "HEAD", "--"}
		for _, p := range keep {
			args = append(args, ":(literal)"+p)
		}
		var cut bool
		patch, cut, err = gitRO(ctx, wt.Path, maxBytesFor(limit), args...)
		if err != nil {
			return meta, err
		}
		if cut {
			return meta, fmt.Errorf("snapshot passaria de maxMB=%d; nada foi gravado", sc.MaxMB)
		}
	}
	total += int64(len(patch))
	if total > limit {
		return meta, fmt.Errorf("snapshot passaria de maxMB=%d; nada foi gravado", sc.MaxMB)
	}

	now := snapshotNow()
	base := snapshotRunDir(cfg, r.Key, folder)
	dir := filepath.Join(base, now.Format(snapshotTimeLayout))
	for i := 2; exists(dir); i++ {
		dir = filepath.Join(base, fmt.Sprintf("%s-%d", now.Format(snapshotTimeLayout), i))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return meta, err
	}
	cleanup := func(err error) (SnapshotMeta, error) {
		_ = os.RemoveAll(dir)
		return SnapshotMeta{}, err
	}

	meta = SnapshotMeta{
		Repo:      r.Key,
		Folder:    folder,
		Path:      wt.Path,
		Branch:    wt.Branch,
		Head:      wt.Head,
		Upstream:  wt.Upstream,
		Base:      r.Base,
		Dirty:     wt.Dirty,
		Unpushed:  wt.Unpushed,
		Omitted:   omitted,
		Agents:    snapshotAgents(ctx, cfg, wt.Path),
		Note:      note,
		Reason:    reason,
		CreatedAt: now,
	}
	if meta.Omitted == nil {
		meta.Omitted = []string{}
	}
	if meta.Agents == nil {
		meta.Agents = []SnapshotAgent{}
	}
	if mb, _, err := gitRO(ctx, wt.Path, 1<<10, "merge-base", "HEAD", r.Base); err == nil {
		meta.MergeBase = strings.TrimSpace(mb)
	}
	if conn := snapshotConnection(cfg, wt); conn != nil {
		meta.LastSavepoint = conn.LastSavepoint
		meta.WtNote = conn.Note
	}

	if patch != "" {
		if err := os.WriteFile(filepath.Join(dir, "changes.patch"), []byte(patch), 0o600); err != nil {
			return cleanup(err)
		}
		meta.HasPatch = true
	}
	tarBytes, err := writeUntrackedTar(filepath.Join(dir, "untracked.tar"), wt.Path, files, now)
	if err != nil {
		return cleanup(err)
	}
	if tarBytes > 0 {
		meta.HasUntracked = true
	}
	meta.Bytes = int64(len(patch)) + tarBytes

	if wt.Unpushed > 0 {
		bundle := filepath.Join(dir, "commits.bundle")
		if _, err := git(ctx, wt.Path, "bundle", "create", bundle, "HEAD", "--not", "--remotes"); err != nil {
			return cleanup(err)
		}
		if err := os.Chmod(bundle, 0o600); err != nil {
			return cleanup(err)
		}
		if fi, err := os.Stat(bundle); err == nil {
			meta.HasBundle = true
			meta.Bytes += fi.Size()
		}
	}

	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return cleanup(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), append(b, '\n'), 0o600); err != nil {
		return cleanup(err)
	}
	meta.Dir = dir

	pruneSnapshots(cfg, r.Key, folder)
	if lg != nil {
		rel := relSlash(cfg.Root, wt.Path)
		lg.Log(r.Key, "ok", fmt.Sprintf("snapshot de %s em %s (%d omitidos)", rel, filepath.Base(dir), len(meta.Omitted)))
	}
	return meta, nil
}

func writeUntrackedTar(tarPath, root string, files []snapshotFile, now time.Time) (int64, error) {
	if len(files) == 0 {
		return 0, nil
	}
	f, err := os.OpenFile(tarPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	tw := tar.NewWriter(f)
	var total int64
	for _, sf := range files {
		if !within(root, filepath.Join(root, filepath.FromSlash(sf.Name))) {
			continue
		}
		h := &tar.Header{Name: sf.Name, Mode: int64(sf.Mode) & 0o777, Size: sf.Size, ModTime: now, Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(h); err != nil {
			_ = tw.Close()
			_ = f.Close()
			return 0, err
		}
		src, err := os.Open(filepath.Join(root, filepath.FromSlash(sf.Name)))
		if err != nil {
			_ = tw.Close()
			_ = f.Close()
			return 0, err
		}
		n, err := io.Copy(tw, src)
		_ = src.Close()
		if err != nil {
			_ = tw.Close()
			_ = f.Close()
			return 0, err
		}
		total += n
	}
	if err := tw.Close(); err != nil {
		_ = f.Close()
		return 0, err
	}
	return total, f.Close()
}

// snapshotConnection lê só a nota desta worktree (sem gravar nada) e aplica o
// vínculo automático de savepoint.
func snapshotConnection(cfg *Config, wt WorktreeInfo) *Connection {
	c := &Connection{}
	if cfg.Vault != nil && cfg.Vault.Path != "" {
		if root, err := filepath.EvalSymlinks(cfg.Vault.Path); err == nil {
			dir := filepath.Join(root, filepath.FromSlash(vaultFolder))
			p := filepath.Join(dir, noteName(wt)+".md")
			if within(dir, p) && !isLink(p) {
				if n, err := readNote(p); err == nil && n.WT.Connection != nil {
					*c = *n.WT.Connection
				}
			}
		}
	}
	applySavepointLink(c, loadSavepointLinks(cfg).find(wt, noteName(wt)))
	return c
}

// snapshotAgents copia as sessões registradas nesta worktree: são o que
// permite retomar o agente depois de restaurar.
func snapshotAgents(ctx context.Context, cfg *Config, wtPath string) []SnapshotAgent {
	dir := filepath.Join(stateRoot(cfg), "agents")
	st := collectAgents(buildWtIndex(ctx, cfg), loadAgentRecords(dir, time.Now()), scanProcAgents(), dir)
	var out []SnapshotAgent
	seen := map[string]bool{}
	for _, a := range st.Agents {
		if a.Session == "" || !samePath(a.Path, wtPath) {
			continue
		}
		key := a.Agent + "/" + a.Session
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, SnapshotAgent{Agent: a.Agent, Session: a.Session, State: a.State})
	}
	return out
}

func readSnapshotMeta(dir string) (SnapshotMeta, error) {
	var m SnapshotMeta
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	return m, nil
}

// listSnapshots devolve os snapshots de uma pasta (folder vazio = todas as
// pastas do repo; repo vazio = todos os repos), do mais novo para o mais velho.
func listSnapshots(cfg *Config, repo, folder string) []SnapshotMeta {
	out := []SnapshotMeta{}
	scanFolder := func(base string) {
		ents, err := os.ReadDir(base)
		if err != nil {
			return
		}
		for _, e := range ents {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			dir := filepath.Join(base, e.Name())
			if isLink(dir) {
				continue
			}
			m, err := readSnapshotMeta(dir)
			if err != nil {
				continue
			}
			m.Dir = dir
			out = append(out, m)
		}
	}
	if folder != "" {
		scanFolder(snapshotRunDir(cfg, repo, folder))
	} else if repo != "" {
		// Lista um nível abaixo do repo (as pastas de worktree).
		repoDir := filepath.Join(snapshotsRoot(cfg), safeSnapshotSeg(repo))
		ents, _ := os.ReadDir(repoDir)
		for _, e := range ents {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				scanFolder(filepath.Join(repoDir, e.Name()))
			}
		}
	} else {
		repos, _ := os.ReadDir(snapshotsRoot(cfg))
		for _, r := range repos {
			if !r.IsDir() || strings.HasPrefix(r.Name(), ".") {
				continue
			}
			repoDir := filepath.Join(snapshotsRoot(cfg), r.Name())
			features, _ := os.ReadDir(repoDir)
			for _, f := range features {
				if f.IsDir() && !strings.HasPrefix(f.Name(), ".") {
					scanFolder(filepath.Join(repoDir, f.Name()))
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func removeSnapshotDir(cfg *Config, dir string) {
	root := snapshotsRoot(cfg)
	if !within(root, dir) || samePath(root, dir) || isLink(dir) {
		return
	}
	if _, err := os.Stat(filepath.Join(dir, "meta.json")); err != nil {
		return
	}
	_ = os.RemoveAll(dir)
}

// pruneSnapshots aplica a retenção logo depois de criar um snapshot: idade por
// motivo e, entre os manuais, só os Keep mais novos por pasta.
func pruneSnapshots(cfg *Config, repo, folder string) {
	sc := snapshotCfg(cfg)
	now := snapshotNow()
	base := snapshotRunDir(cfg, repo, folder)
	if !within(snapshotsRoot(cfg), base) || isLink(base) {
		return
	}
	ents, err := os.ReadDir(base)
	if err != nil {
		return
	}
	type item struct {
		dir  string
		meta SnapshotMeta
	}
	var items []item
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(base, e.Name())
		m, err := readSnapshotMeta(dir)
		if err != nil {
			continue
		}
		days := sc.MaxAgeDays
		if m.Reason == "pre-remove" {
			days = sc.PreRemoveDays
		}
		if days > 0 && now.Sub(m.CreatedAt) > time.Duration(days)*24*time.Hour {
			removeSnapshotDir(cfg, dir)
			continue
		}
		items = append(items, item{dir, m})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].meta.CreatedAt.After(items[j].meta.CreatedAt) })
	kept := 0
	for _, it := range items {
		if it.meta.Reason == "pre-remove" {
			continue
		}
		kept++
		if kept > sc.Keep {
			removeSnapshotDir(cfg, it.dir)
		}
	}
}

// ---------- restauração ----------

func checkTarPath(root, name string) (string, error) {
	n := filepath.ToSlash(name)
	if n == "" || filepath.IsAbs(name) || strings.HasPrefix(n, "/") || n == ".." ||
		strings.HasPrefix(n, "../") || strings.Contains(n, "/../") ||
		n == ".git" || strings.HasPrefix(n, ".git/") {
		return "", fmt.Errorf("caminho inválido no snapshot: %q", name)
	}
	target := filepath.Join(root, filepath.FromSlash(n))
	if !within(root, target) || samePath(target, root) {
		return "", fmt.Errorf("caminho fora da worktree no snapshot: %q", name)
	}
	return target, nil
}

// checkNoLinkParents recusa caminhos cujo pai existente seja link: extrair ali
// poderia escrever fora da worktree.
func checkNoLinkParents(root, target string) error {
	for p := filepath.Dir(target); within(root, p) && !samePath(root, p); p = filepath.Dir(p) {
		if isLink(p) {
			return fmt.Errorf("caminho passa por link: %s", relSlash(root, p))
		}
	}
	return nil
}

func validateTar(tarPath, root string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := checkTarPath(root, h.Name)
		if err != nil {
			return err
		}
		if err := checkNoLinkParents(root, target); err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
		case tar.TypeReg:
			if _, err := os.Lstat(target); err == nil {
				return fmt.Errorf("snapshot extrairia sobre %s; a worktree não está vazia nesse caminho", h.Name)
			}
		default:
			return fmt.Errorf("tipo de entrada não suportado no snapshot: %s", h.Name)
		}
	}
}

func extractTar(tarPath, root string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := checkTarPath(root, h.Name)
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(h.Mode).Perm() & 0o777
		if mode == 0 {
			mode = 0o644
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		_, cerr := io.Copy(out, tr)
		cerr = errors.Join(cerr, out.Close())
		if cerr != nil {
			return cerr
		}
	}
}

func restoreSnapshot(ctx context.Context, cfg *Config, path, dir string, lg Logger) error {
	meta, err := readSnapshotMeta(dir)
	if err != nil {
		return fmt.Errorf("snapshot ilegível: %w", err)
	}
	if !within(snapshotsRoot(cfg), dir) {
		return errors.New("snapshot fora da pasta de snapshots")
	}
	st, err := worktreeStatus(ctx, path)
	if err != nil {
		return err
	}
	if st.Dirty > 0 {
		return fmt.Errorf("worktree tem %d alterações; faça commit ou descarte antes de restaurar", st.Dirty)
	}
	patchPath := filepath.Join(dir, "changes.patch")
	tarPath := filepath.Join(dir, "untracked.tar")
	if exists(patchPath) {
		if _, err := git(ctx, path, "apply", "--check", patchPath); err != nil {
			return fmt.Errorf("patch não aplica nesta worktree: %w", err)
		}
	}
	if exists(tarPath) {
		if err := validateTar(tarPath, path); err != nil {
			return err
		}
	}
	if exists(patchPath) {
		if _, err := git(ctx, path, "apply", "--3way", patchPath); err != nil {
			return err
		}
		if lg != nil {
			lg.Log(meta.Repo, "ok", "changes.patch aplicado")
		}
	}
	if exists(tarPath) {
		if err := extractTar(tarPath, path); err != nil {
			return err
		}
		if lg != nil {
			lg.Log(meta.Repo, "ok", "untracked.tar extraído")
		}
	}
	if exists(filepath.Join(dir, "commits.bundle")) && lg != nil {
		lg.Log(meta.Repo, "info", "commits do bundle não são aplicados; rode: git fetch "+filepath.Join(dir, "commits.bundle")+" "+meta.Branch)
	}
	return nil
}

// ---------- HTTP ----------

func (s *server) snapshotTarget(ctx context.Context, repo, path string) (*RepoConfig, WorktreeInfo, error) {
	rc := s.cfg.repo(repo)
	if rc == nil {
		return nil, WorktreeInfo{}, errors.New("repo desconhecido")
	}
	g, ok := s.worktreeOf(ctx, rc, path)
	if !ok || !within(s.cfg.Root, g.Path) {
		return nil, WorktreeInfo{}, errors.New("caminho não é uma worktree deste repo")
	}
	folder := filepath.Base(g.Path)
	wt := WorktreeInfo{
		Repo: rc.Key, Feature: s.cfg.featureFromFolder(rc, folder), Folder: folder,
		Path: g.Path, Branch: g.Branch, Head: g.Head, Unpushed: unpushedCount(ctx, g.Path),
	}
	if st, err := worktreeStatus(ctx, g.Path); err == nil {
		wt.Upstream, wt.Dirty = st.Upstream, st.Dirty
	}
	return rc, wt, nil
}

func (s *server) opSnapshot(ctx context.Context, r *http.Request, lg Logger) error {
	var req struct {
		Repo   string `json:"repo"`
		Path   string `json:"path"`
		Note   string `json:"note"`
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	rc, wt, err := s.snapshotTarget(ctx, req.Repo, req.Path)
	if err != nil {
		return err
	}
	_, err = createSnapshot(ctx, s.cfg, rc, wt, req.Reason, req.Note, lg)
	return err
}

func (s *server) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repo := q.Get("repo")
	if repo != "" && s.cfg.repo(repo) == nil {
		http.Error(w, "repo desconhecido", http.StatusBadRequest)
		return
	}
	folder := ""
	if p := q.Get("path"); p != "" {
		if repo == "" {
			http.Error(w, "path exige repo", http.StatusBadRequest)
			return
		}
		folder = filepath.Base(filepath.Clean(filepath.FromSlash(p)))
	}
	writeJSON(w, listSnapshots(s.cfg, repo, folder))
}

func (s *server) opSnapshotRestore(ctx context.Context, r *http.Request, lg Logger) error {
	var req struct {
		Repo     string `json:"repo"`
		Path     string `json:"path"`
		Snapshot string `json:"snapshot"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	rc, wt, err := s.snapshotTarget(ctx, req.Repo, req.Path)
	if err != nil {
		return err
	}
	name := req.Snapshot
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return errors.New("snapshot inválido")
	}
	dir := filepath.Join(snapshotRunDir(s.cfg, rc.Key, wt.Folder), name)
	if !within(snapshotsRoot(s.cfg), dir) || isLink(dir) {
		return errors.New("snapshot inválido")
	}
	if _, err := os.Stat(filepath.Join(dir, "meta.json")); err != nil {
		return errors.New("snapshot não encontrado")
	}
	lg.Log(rc.Key, "info", "restaurando snapshot "+name+" em "+relSlash(s.cfg.Root, wt.Path))
	return restoreSnapshot(ctx, s.cfg, wt.Path, dir, lg)
}
