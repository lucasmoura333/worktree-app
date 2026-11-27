package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Arquivos ignorados pelo git somem com a worktree sem contar como alteração.
// Deps, links e cópias iguais às do main se recriam; o resto vira aviso.

const ignoredMax = 50

type IgnoredFile struct {
	Name string `json:"name"`
	Note string `json:"note,omitempty"`
}

type IgnoredResp struct {
	Files []IgnoredFile `json:"files"`
	More  int           `json:"more,omitempty"`
}

func ignoredLosses(ctx context.Context, cfg *Config, r *RepoConfig, wtPath string) (IgnoredResp, error) {
	resp := IgnoredResp{Files: []IgnoredFile{}}
	out, _, err := gitRO(ctx, wtPath, 1<<20, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return resp, err
	}
	main := cfg.mainPath(r)
	for _, n := range strings.Split(out, "\x00") {
		name := strings.TrimSuffix(n, "/")
		if name == "" || isDep(r, name) || isLink(filepath.Join(wtPath, filepath.FromSlash(name))) {
			continue
		}
		note := ""
		switch {
		case isCopyItem(r, name) && strings.HasSuffix(n, "/"):
			note = "copiada do main"
		case isCopyItem(r, name):
			if sameAsMain(r, main, wtPath, name) {
				continue
			}
			note = "difere do main"
		case strings.HasSuffix(n, "/"):
			note = "pasta"
		}
		if len(resp.Files) == ignoredMax {
			resp.More++
			continue
		}
		resp.Files = append(resp.Files, IgnoredFile{Name: name, Note: note})
	}
	return resp, nil
}

func isDep(r *RepoConfig, name string) bool {
	for _, d := range r.Deps {
		if dd := strings.TrimSuffix(filepath.ToSlash(d.Dir), "/"); name == dd || strings.HasPrefix(name, dd+"/") {
			return true
		}
	}
	return false
}

func isCopyItem(r *RepoConfig, name string) bool {
	for _, c := range r.Copy {
		if strings.TrimSuffix(filepath.ToSlash(c), "/") == name {
			return true
		}
	}
	return false
}

// sameAsMain compara com o original; no .env do docker as chaves que o
// setup reescreve (projeto e porta) não contam.
func sameAsMain(r *RepoConfig, main, wt, name string) bool {
	a, err1 := os.ReadFile(filepath.Join(main, filepath.FromSlash(name)))
	b, err2 := os.ReadFile(filepath.Join(wt, filepath.FromSlash(name)))
	if err1 != nil || err2 != nil {
		return false
	}
	if d := r.DockerEnv; d != nil && filepath.ToSlash(d.File) == name {
		a, b = dropEnvKeys(a, d.ProjectKey, d.PortKey), dropEnvKeys(b, d.ProjectKey, d.PortKey)
	}
	return bytes.Equal(a, b)
}

func dropEnvKeys(b []byte, keys ...string) []byte {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		k := envLineKey(line)
		drop := false
		for _, key := range keys {
			drop = drop || (key != "" && k == key)
		}
		if !drop {
			out = append(out, line)
		}
	}
	return []byte(strings.Join(out, "\n"))
}

func (s *server) handleIgnored(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rc := s.cfg.repo(q.Get("repo"))
	if rc == nil {
		http.Error(w, "repo desconhecido", http.StatusBadRequest)
		return
	}
	g, ok := s.worktreeOf(r.Context(), rc, q.Get("path"))
	if !ok || samePath(g.Path, s.cfg.mainPath(rc)) {
		http.Error(w, "caminho não é worktree deste repo", http.StatusBadRequest)
		return
	}
	resp, err := ignoredLosses(r.Context(), s.cfg, rc, g.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, resp)
}
