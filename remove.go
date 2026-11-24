package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type RemoveReq struct {
	Feature      string   `json:"feature"`
	Repos        []string `json:"repos"`
	Force        bool     `json:"force"`
	DeleteBranch bool     `json:"deleteBranch"`
	ForceBranch  bool     `json:"forceBranch"`
	Workspace    bool     `json:"workspace"`
	// SkipSnapshot desmarca "salvar snapshot antes" no diálogo de remoção.
	SkipSnapshot bool `json:"skipSnapshot,omitempty"`
}

func removeFeature(ctx context.Context, cfg *Config, req *RemoveReq, lg Logger) error {
	st := scanState(ctx, cfg)
	var f *FeatureState
	for i := range st.Features {
		if st.Features[i].Name == req.Feature {
			f = &st.Features[i]
		}
	}
	if f == nil {
		return fmt.Errorf("feature %q não encontrada", req.Feature)
	}
	want := map[string]bool{}
	for _, k := range req.Repos {
		want[k] = true
	}
	var targets []WorktreeInfo
	for _, wt := range f.Worktrees {
		if len(want) == 0 || want[wt.Repo] {
			targets = append(targets, wt)
		}
	}
	if len(targets) == 0 {
		return errors.New("nenhuma worktree selecionada")
	}

	// Checa tudo antes de apagar qualquer coisa: ou remove tudo, ou nada.
	blocked := false
	for _, wt := range targets {
		for _, risk := range removalRisks(wt) {
			blocked = true
			lg.Log(wt.Repo, map[bool]string{true: "warn", false: "error"}[req.Force], risk)
		}
	}
	if blocked && !req.Force {
		return errors.New("nada foi removido; marque \"forçar\" para remover mesmo assim")
	}

	// Remoção forçada com risco: snapshot de cada alvo antes de apagar nada.
	// Se um snapshot falhar, a remoção inteira é abortada.
	if blocked && req.Force && !req.SkipSnapshot {
		for _, wt := range targets {
			if _, err := createSnapshot(ctx, cfg, cfg.repo(wt.Repo), wt, "pre-remove", "", lg); err != nil {
				return fmt.Errorf("snapshot de %s falhou; nada foi removido: %w", wt.Folder, err)
			}
		}
	}

	errs := make([]error, len(targets))
	var wg sync.WaitGroup
	for i := range targets {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			wt := targets[i]
			errs[i] = removeWorktree(ctx, cfg, cfg.repo(wt.Repo), wt, req, lg)
			if errs[i] != nil {
				lg.Log(wt.Repo, "error", errs[i].Error())
			}
		}(i)
	}
	wg.Wait()
	err := errors.Join(errs...)

	if req.Workspace && err == nil && len(targets) == len(f.Worktrees) {
		removeFeatureWorkspaces(cfg, f, targets, lg)
	}
	if f.TasksDir != "" {
		lg.Log("", "info", f.TasksDir+" mantida (notas não são apagadas)")
	}
	return err
}

func removalRisks(wt WorktreeInfo) []string {
	var out []string
	if wt.Error != "" {
		out = append(out, "status indisponível: "+wt.Error)
	}
	if wt.Dirty > 0 {
		out = append(out, fmt.Sprintf("%s tem %d alterações não commitadas", wt.Folder, wt.Dirty))
	}
	switch {
	case wt.Unpushed > 0:
		out = append(out, fmt.Sprintf("%s tem %d commits que não estão em nenhum remoto", wt.Folder, wt.Unpushed))
	case wt.Unpushed < 0 && wt.Error == "":
		out = append(out, wt.Folder+": não deu para conferir commits fora do remoto")
	}
	return out
}

func removeWorktree(ctx context.Context, cfg *Config, r *RepoConfig, wt WorktreeInfo, req *RemoveReq, lg Logger) error {
	force := req.Force
	main := cfg.mainPath(r)
	// Worktrees montadas à mão podem ter deps como junction para o main. Elas
	// saem antes, sozinhas, para nada que apague a pasta entrar no original.
	for _, d := range r.Deps {
		p := filepath.Join(wt.Path, d.Dir)
		if !isLink(p) {
			continue
		}
		if err := os.Remove(p); err != nil {
			return fmt.Errorf("não deu para remover a junction %s: %w", d.Dir, err)
		}
		lg.Log(r.Key, "info", d.Dir+": junction removida (o original segue intacto)")
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, wt.Path)
	lg.Log(r.Key, "info", "removendo "+relSlash(cfg.Root, wt.Path)+"…")
	if _, err := git(ctx, main, args...); err != nil {
		if isDir(wt.Path) {
			// O git falha em caminhos longos do node_modules; se a worktree segue
			// limpa (ou foi forçado), apagar a pasta direto é seguro.
			if !force {
				if st, serr := worktreeStatus(ctx, wt.Path); serr != nil || ownDirty(r, wt.Path, st) > 0 {
					return err
				}
			}
			lg.Log(r.Key, "warn", "git worktree remove falhou ("+err.Error()+"); apagando a pasta direto")
			if rerr := removeTree(ctx, wt.Path); rerr != nil {
				return rerr
			}
		}
		if _, perr := git(ctx, main, "worktree", "prune"); perr != nil {
			return perr
		}
	}
	lg.Log(r.Key, "ok", "removida "+relSlash(cfg.Root, wt.Path))
	if req.DeleteBranch && wt.Branch != "" {
		deleteLocalBranch(ctx, main, r.Key, wt.Branch, req.ForceBranch, lg)
	}
	return nil
}

// deleteLocalBranch apaga só a branch local; a do remoto nunca é tocada.
// Sem force usa -d, que o git recusa se a branch não estiver mesclada.
func deleteLocalBranch(ctx context.Context, main, repo, branch string, force bool, lg Logger) {
	flag := "-d"
	if force {
		flag = "-D"
	}
	out, err := git(ctx, main, "branch", flag, branch)
	if err != nil {
		lg.Log(repo, "warn", "branch "+branch+" mantida: "+err.Error())
		return
	}
	// "Deleted branch x (was abc123)": o hash permite recriar com git branch x abc123.
	lg.Log(repo, "ok", strings.TrimSpace(out))
}

// removeFeatureWorkspaces só apaga workspaces que apontam exclusivamente para
// o que acabou de ser removido (mais a pasta de tasks da feature).
func removeFeatureWorkspaces(cfg *Config, f *FeatureState, removed []WorktreeInfo, lg Logger) {
	allowed := map[string]bool{pathKey(cfg.tasksPath(f.Name)): true}
	for _, wt := range removed {
		allowed[pathKey(wt.Path)] = true
	}
	for _, name := range f.Workspaces {
		p := filepath.Join(cfg.Root, name)
		paths, err := workspaceFolderPaths(cfg.Root, p)
		if err != nil {
			lg.Log("", "warn", name+" mantido: "+err.Error())
			continue
		}
		only := len(paths) > 0
		for _, fp := range paths {
			if !allowed[pathKey(fp)] {
				only = false
			}
		}
		if !only {
			lg.Log("", "info", name+" mantido: aponta para outras pastas também")
			continue
		}
		if err := os.Remove(p); err != nil {
			lg.Log("", "warn", name+": "+err.Error())
			continue
		}
		lg.Log("", "ok", "removido "+name)
	}
}

func pruneRepos(ctx context.Context, cfg *Config, keys []string, lg Logger) error {
	var errs []error
	for _, r := range selectRepos(cfg, keys) {
		out, err := git(ctx, cfg.mainPath(r), "worktree", "prune", "-v")
		if err != nil {
			lg.Log(r.Key, "error", err.Error())
			errs = append(errs, err)
			continue
		}
		msg := lastLines(out, 20)
		if msg == "" {
			msg = "nada para limpar"
		}
		lg.Log(r.Key, "ok", "prune: "+msg)
	}
	return errors.Join(errs...)
}

func fetchRepos(ctx context.Context, cfg *Config, keys []string, lg Logger) error {
	repos := selectRepos(cfg, keys)
	errs := make([]error, len(repos))
	var wg sync.WaitGroup
	for i, r := range repos {
		wg.Add(1)
		go func(i int, r *RepoConfig) {
			defer wg.Done()
			start := time.Now()
			if _, err := git(ctx, cfg.mainPath(r), "fetch", "origin", "--prune"); err != nil {
				errs[i] = err
				lg.Log(r.Key, "error", err.Error())
				return
			}
			lg.Log(r.Key, "ok", "fetch em "+since(start))
		}(i, r)
	}
	wg.Wait()
	return errors.Join(errs...)
}

func selectRepos(cfg *Config, keys []string) []*RepoConfig {
	var out []*RepoConfig
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	for i := range cfg.Repos {
		if len(want) == 0 || want[cfg.Repos[i].Key] {
			out = append(out, &cfg.Repos[i])
		}
	}
	return out
}
