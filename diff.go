package main

// Diff só leitura: uma worktree contra a base do repo (incluindo o que ainda
// não foi commitado) ou contra outra worktree do mesmo repo (commits). Usa
// GIT_OPTIONAL_LOCKS=0 para nunca criar index.lock, corta o patch em
// diffMaxKB e deixa de fora os arquivos do "copy" (.env e afins).

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	diffDefaultMaxKB = 2048
	diffMaxFiles     = 2000
	diffTimeout      = 15 * time.Second
)

type DiffFile struct {
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"`
	Add     int    `json:"add"`
	Del     int    `json:"del"`
	Binary  bool   `json:"binary,omitempty"`
}

type DiffResp struct {
	Repo       string     `json:"repo"`
	From       string     `json:"from"`
	To         string     `json:"to"`
	FromLabel  string     `json:"fromLabel"`
	ToLabel    string     `json:"toLabel"`
	Files      []DiffFile `json:"files"`
	Untracked  []string   `json:"untracked"`
	Patch      string     `json:"patch"`
	Truncated  bool       `json:"truncated,omitempty"`
	FilesCut   bool       `json:"filesCut,omitempty"`
	DirtyFrom  int        `json:"dirtyFrom"`
	DirtyTo    int        `json:"dirtyTo"`
	WorkingDir bool       `json:"workingDir"`
}

// gitRO roda git sem locks opcionais e devolve no máximo max bytes da saída.
func gitRO(ctx context.Context, dir string, max int, args ...string) (string, bool, error) {
	var errb bytes.Buffer
	cmd := command(ctx, dir, "git", args...)
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0")
	cmd.Stderr = &errb
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", false, err
	}
	if err := cmd.Start(); err != nil {
		return "", false, err
	}
	b, _ := io.ReadAll(io.LimitReader(out, int64(max)+1))
	truncated := len(b) > max
	if truncated {
		b = b[:max]
		_ = cmd.Process.Kill() // não precisa do resto
	}
	_, _ = io.Copy(io.Discard, out)
	werr := cmd.Wait()
	if werr != nil && !truncated {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = werr.Error()
		}
		return string(b), false, &gitErr{msg}
	}
	return string(b), truncated, nil
}

type gitErr struct{ msg string }

func (e *gitErr) Error() string { return "git: " + e.msg }

// parseNumstatZ lê "git diff --numstat -z -M".
func parseNumstatZ(s string, limit int) ([]DiffFile, bool) {
	files := []DiffFile{}
	parts := strings.Split(s, "\x00")
	for i := 0; i < len(parts); i++ {
		head := parts[i]
		if head == "" {
			continue
		}
		f := strings.SplitN(head, "\t", 3)
		if len(f) < 3 {
			continue
		}
		df := DiffFile{}
		if f[0] == "-" && f[1] == "-" {
			df.Binary = true
		} else {
			df.Add, _ = strconv.Atoi(f[0])
			df.Del, _ = strconv.Atoi(f[1])
		}
		if f[2] == "" && i+2 < len(parts) { // renome: "\0velho\0novo"
			df.OldPath, df.Path = parts[i+1], parts[i+2]
			i += 2
		} else {
			df.Path = f[2]
		}
		if len(files) >= limit {
			return files, true
		}
		files = append(files, df)
	}
	return files, false
}

func excludeSpecs(r *RepoConfig) []string {
	specs := []string{"--", "."}
	for _, c := range r.Copy {
		specs = append(specs, ":(exclude)"+filepath.ToSlash(c))
	}
	for _, d := range r.Deps {
		specs = append(specs, ":(exclude)"+filepath.ToSlash(d.Dir))
	}
	return specs
}

func excludedName(r *RepoConfig, name string) bool {
	for _, c := range r.Copy {
		c = strings.TrimSuffix(filepath.ToSlash(c), "/")
		if name == c || strings.HasPrefix(name, c+"/") {
			return true
		}
	}
	for _, d := range r.Deps {
		if dd := filepath.ToSlash(d.Dir); name == dd || strings.HasPrefix(name, dd+"/") {
			return true
		}
	}
	return false
}

func (s *server) worktreeOf(ctx context.Context, r *RepoConfig, p string) (gitWorktree, bool) {
	if p == "" {
		return gitWorktree{}, false
	}
	list, err := listWorktrees(ctx, s.cfg.mainPath(r))
	if err != nil {
		return gitWorktree{}, false
	}
	p = filepath.Clean(filepath.FromSlash(p))
	for _, g := range list {
		if samePath(g.Path, p) && !g.Bare && isDir(g.Path) {
			return g, true
		}
	}
	return gitWorktree{}, false
}

func (s *server) handleDiff(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), diffTimeout)
	defer cancel()
	q := r.URL.Query()
	rc := s.cfg.repo(q.Get("repo"))
	if rc == nil {
		http.Error(w, "repo desconhecido", http.StatusBadRequest)
		return
	}
	a, ok := s.worktreeOf(ctx, rc, q.Get("path"))
	if !ok {
		http.Error(w, "caminho não é worktree deste repo", http.StatusBadRequest)
		return
	}
	maxBytes := s.cfg.DiffMaxKB << 10
	resp := DiffResp{Repo: rc.Key, From: a.Path, Files: []DiffFile{}, Untracked: []string{}}
	if st, err := worktreeStatus(ctx, a.Path); err == nil {
		resp.DirtyFrom = st.Dirty
	}
	specs := excludeSpecs(rc)
	flags := []string{"-M", "--no-ext-diff", "--no-textconv", "--no-color"}

	var revs []string
	against := q.Get("against")
	if against == "" || against == "base" {
		mb, _, err := gitRO(ctx, a.Path, 1<<10, "merge-base", "HEAD", rc.Base)
		if err != nil {
			http.Error(w, "sem merge-base com "+rc.Base+": "+err.Error(), http.StatusBadRequest)
			return
		}
		mb = strings.TrimSpace(mb)
		resp.To, resp.ToLabel = mb, rc.Base+" (merge-base "+shortHash(mb)+")"
		resp.FromLabel = filepath.Base(a.Path) + " (com o que não foi commitado)"
		resp.WorkingDir = true
		// <mb> sozinho compara com a árvore de trabalho: entra o que não foi commitado.
		revs = []string{mb}
		if out, _, err := gitRO(ctx, a.Path, 1<<20, "ls-files", "--others", "--exclude-standard", "-z"); err == nil {
			for _, n := range strings.Split(out, "\x00") {
				if n != "" && !excludedName(rc, n) && len(resp.Untracked) < diffMaxFiles {
					resp.Untracked = append(resp.Untracked, n)
				}
			}
		}
	} else {
		b, ok := s.worktreeOf(ctx, rc, against)
		if !ok {
			http.Error(w, "against não é worktree deste repo", http.StatusBadRequest)
			return
		}
		if st, err := worktreeStatus(ctx, b.Path); err == nil {
			resp.DirtyTo = st.Dirty
		}
		resp.To = b.Path
		resp.FromLabel = filepath.Base(a.Path) + " @ " + shortHash(a.Head)
		resp.ToLabel = filepath.Base(b.Path) + " @ " + shortHash(b.Head)
		// do outro lado para este: "+" é o que esta worktree tem a mais.
		revs = []string{b.Head, a.Head}
	}

	ns, _, err := gitRO(ctx, a.Path, 4<<20, append(append(append([]string{"diff", "--numstat", "-z"}, flags...), revs...), specs...)...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp.Files, resp.FilesCut = parseNumstatZ(ns, diffMaxFiles)
	patch, cut, err := gitRO(ctx, a.Path, maxBytes, append(append(append([]string{"diff"}, flags...), revs...), specs...)...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp.Patch, resp.Truncated = patch, cut
	writeJSON(w, resp)
}
