package main

import (
	"io/fs"
	"net/http"
	"strings"
	"time"
)

// Demo data never goes through scanState or syncVault, and has no mutation routes.
func demoHandler(cfg *Config) http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServerFS(sub))
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, demoState()) })
	mux.HandleFunc("POST /api/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Prévia visual: operação desativada", http.StatusForbidden)
	})
	return newServer(cfg).guard(mux)
}
func demoState() *State {
	st := &State{Demo: true, Root: "Prévia visual", Vault: "Obsidian Vault", VaultScope: strings.TrimSuffix(docScope, "/"), Repos: []RepoState{{Key: "app", Dir: "acme-app", Main: "dev", Base: "origin/dev"}, {Key: "api", Dir: "acme-api", Main: "dev", Base: "origin/dev"}, {Key: "security", Dir: "acme-security", Main: "dev", Base: "origin/dev"}}, Features: []FeatureState{}, Stale: []WorktreeInfo{}, Archived: []WorktreeInfo{}}
	samples := []struct {
		feature, repo, branch, front, sp string
		dirty, behind, port              int
	}{
		{"checkout-novo", "app", "dev.checkout-novo.consolidado", "checkout", "2026-11-10-checkout-novo", 2, 3, 5183},
		{"menu-lateral", "app", "dev.menu-lateral", "checkout", "2026-11-09-menu-lateral", 0, 0, 5184},
		{"auditoria-login", "security", "dev.auditoria-login", "seguranca", "2026-11-11-auditoria-login", 0, 0, 0},
		{"webhook-retry", "api", "dev.webhook-retry", "notificacoes", "", 1, 0, 5185},
	}
	for _, sample := range samples {
		wt := WorktreeInfo{Feature: sample.feature, Repo: sample.repo, RepoDir: "acme-" + sample.repo, Branch: sample.branch, Head: "1a2b3c4d5", Upstream: "origin/dev", Dirty: sample.dirty, Behind: sample.behind, Port: sample.port, MissingFiles: []string{}, MissingDeps: []string{}, LinkedDeps: []string{}, Connection: &Connection{Note: vaultFolder + "/" + safeNoteName(sample.feature, sample.repo), Front: "[[" + docScope + "docs/kanbans/" + sample.front + "]]", UpdatedAt: time.Now().Format(time.RFC3339)}}
		if sample.sp != "" {
			wt.Connection.LastSavepoint = "[[" + docScope + "docs/savepoints/" + sample.sp + "]]"
		}
		st.Features = append(st.Features, FeatureState{Name: sample.feature, Worktrees: []WorktreeInfo{wt}, Workspaces: []string{sample.feature + ".code-workspace"}})
	}
	old := st.Features[0].Worktrees[0]
	old.Feature = "checkout-poc"
	old.Branch = "dev.checkout-poc"
	old.Dirty = 0
	old.Connection = &Connection{Note: vaultFolder + "/checkout-poc--app", Front: "[[" + docScope + "docs/kanbans/notificacoes]]", UpdatedAt: time.Now().AddDate(0, 0, -2).Format(time.RFC3339)}
	st.Archived = append(st.Archived, old)
	return st
}
