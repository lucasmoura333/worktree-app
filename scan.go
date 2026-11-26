package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type WorktreeInfo struct {
	Connection    *Connection `json:"connection,omitempty"`
	Port          int         `json:"port,omitempty"`
	DockerProject string      `json:"dockerProject,omitempty"`
	Repo          string      `json:"repo"`
	RepoDir       string      `json:"repoDir"`
	Feature       string      `json:"feature"`
	Folder        string      `json:"folder"`
	Path          string      `json:"path"`
	Branch        string      `json:"branch"`
	Head          string      `json:"head"`
	Detached      bool        `json:"detached"`
	Prunable      bool        `json:"prunable"`
	Locked        bool        `json:"locked,omitempty"`
	Missing       bool        `json:"missing"`
	Dirty         int         `json:"dirty"`
	Upstream      string      `json:"upstream"`
	Ahead         int         `json:"ahead"`
	Behind        int         `json:"behind"`
	Unpushed      int         `json:"unpushed"`
	MissingFiles  []string    `json:"missingFiles"`
	MissingDeps   []string    `json:"missingDeps"`
	LinkedDeps    []string    `json:"linkedDeps"`
	Error         string      `json:"error,omitempty"`
}

type RepoState struct {
	Key        string `json:"key"`
	Dir        string `json:"dir"`
	Main       string `json:"main"`
	MainBranch string `json:"mainBranch"`
	Base       string `json:"base"`
	HasDocker  bool   `json:"hasDocker"`
	Error      string `json:"error,omitempty"`
}

type FeatureState struct {
	Name       string         `json:"name"`
	Worktrees  []WorktreeInfo `json:"worktrees"`
	Workspaces []string       `json:"workspaces"`
	TasksDir   string         `json:"tasksDir"`
}

// ReaderState são as opções do leitor já resolvidas (defaults aplicados).
type ReaderState struct {
	Images         bool   `json:"images"`
	MaxImageWidth  int    `json:"maxImageWidth"`
	Attachments    string `json:"attachments"`
	PDF            string `json:"pdf"`
	ExternalImages bool   `json:"externalImages"`
}

type State struct {
	Demo bool `json:"demo,omitempty"`
	// DocsPreview: prévia estática com leitor e mapa das notas de exemplo.
	DocsPreview bool `json:"docsPreview,omitempty"`
	// Busy: havia uma operação em andamento; vínculos lidos, Vault não gravado.
	Busy       bool           `json:"busy,omitempty"`
	Vault      string         `json:"vault,omitempty"`
	VaultScope string         `json:"vaultScope,omitempty"`
	VaultError string         `json:"vaultError,omitempty"`
	Archived   []WorktreeInfo `json:"archived"`
	Root       string         `json:"root"`
	Repos      []RepoState    `json:"repos"`
	Features   []FeatureState `json:"features"`
	Stale      []WorktreeInfo `json:"stale"`
	Reader     ReaderState    `json:"reader"`
	// Terminal liga os botões de terminal/agente; AgentNames lista os agentes.
	Terminal   bool     `json:"terminal,omitempty"`
	AgentNames []string `json:"agentNames,omitempty"`
}

func scanState(ctx context.Context, cfg *Config) *State {
	type repoScan struct {
		rs  RepoState
		wts []WorktreeInfo
	}
	scans := make([]repoScan, len(cfg.Repos))
	var wg sync.WaitGroup
	for i := range cfg.Repos {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rs, wts := scanRepo(ctx, cfg, &cfg.Repos[i])
			scans[i] = repoScan{rs, wts}
		}(i)
	}
	wg.Wait()

	st := &State{Root: cfg.Root, Features: []FeatureState{}, Stale: []WorktreeInfo{}, Reader: cfg.readerState(), VaultScope: strings.TrimSuffix(docScope, "/"),
		Terminal: len(cfg.Terminal) > 0, AgentNames: cfg.agentNames()}
	byName := map[string]*FeatureState{}
	var order []string
	for _, s := range scans {
		st.Repos = append(st.Repos, s.rs)
		for _, wt := range s.wts {
			if wt.Missing || wt.Prunable {
				st.Stale = append(st.Stale, wt)
				continue
			}
			f := byName[wt.Feature]
			if f == nil {
				f = &FeatureState{Name: wt.Feature, Workspaces: []string{}}
				byName[wt.Feature] = f
				order = append(order, wt.Feature)
			}
			f.Worktrees = append(f.Worktrees, wt)
		}
	}
	sort.Strings(order)
	idx := workspaceIndex(cfg)
	for _, name := range order {
		f := byName[name]
		seen := map[string]bool{}
		add := func(ws string) {
			if !seen[ws] {
				seen[ws] = true
				f.Workspaces = append(f.Workspaces, ws)
			}
		}
		if p := cfg.workspacePath(name); exists(p) {
			add(filepath.Base(p))
		}
		for _, wt := range f.Worktrees {
			for _, ws := range idx[pathKey(wt.Path)] {
				add(ws)
			}
		}
		if td := cfg.tasksPath(name); isDir(td) {
			f.TasksDir = relSlash(cfg.Root, td)
		}
		st.Features = append(st.Features, *f)
	}
	return st
}

func scanRepo(ctx context.Context, cfg *Config, r *RepoConfig) (RepoState, []WorktreeInfo) {
	rs := RepoState{Key: r.Key, Dir: r.Dir, Main: r.Main, Base: r.Base, HasDocker: r.DockerEnv != nil}
	main := cfg.mainPath(r)
	if !isDir(main) {
		rs.Error = "pasta principal " + relSlash(cfg.Root, main) + " não existe"
		return rs, nil
	}
	list, err := listWorktrees(ctx, main)
	if err != nil {
		rs.Error = err.Error()
		return rs, nil
	}
	var infos []WorktreeInfo
	for _, g := range list {
		if g.Bare {
			continue
		}
		if samePath(g.Path, main) {
			rs.MainBranch = g.Branch
			continue
		}
		folder := filepath.Base(g.Path)
		wi := WorktreeInfo{
			Repo: r.Key, RepoDir: r.Dir, Folder: folder, Feature: cfg.featureFromFolder(r, folder),
			Path: g.Path, Branch: g.Branch, Head: shortHash(g.Head), Detached: g.Detached, Prunable: g.Prunable, Locked: g.Locked,
			MissingFiles: []string{}, MissingDeps: []string{}, LinkedDeps: []string{},
		}
		wi.Missing = !isDir(g.Path)
		infos = append(infos, wi)
	}
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i := range infos {
		if infos[i].Missing || infos[i].Prunable {
			continue
		}
		wg.Add(1)
		go func(wi *WorktreeInfo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fillStatus(ctx, r, main, wi)
		}(&infos[i])
	}
	wg.Wait()
	return rs, infos
}

func fillStatus(ctx context.Context, r *RepoConfig, main string, wi *WorktreeInfo) {
	if d := r.DockerEnv; d != nil {
		if b, err := os.ReadFile(filepath.Join(wi.Path, filepath.FromSlash(d.File))); err == nil {
			port, _ := getEnvValue(string(b), d.PortKey)
			wi.Port, _ = strconv.Atoi(port)
			wi.DockerProject, _ = getEnvValue(string(b), d.ProjectKey)
		}
	}

	st, err := worktreeStatus(ctx, wi.Path)
	if err != nil {
		wi.Error = err.Error()
		wi.Unpushed = -1
	} else {
		wi.Dirty, wi.Upstream, wi.Ahead, wi.Behind = ownDirty(r, wi.Path, st), st.Upstream, st.Ahead, st.Behind
		wi.Unpushed = unpushedCount(ctx, wi.Path)
	}
	for _, item := range r.Copy {
		rel := filepath.FromSlash(item)
		if exists(filepath.Join(main, rel)) && !exists(filepath.Join(wi.Path, rel)) {
			wi.MissingFiles = append(wi.MissingFiles, item)
		}
	}
	for _, d := range r.Deps {
		p := filepath.Join(wi.Path, d.Dir)
		switch {
		case isLink(p):
			wi.LinkedDeps = append(wi.LinkedDeps, d.Dir)
		case !exists(p) && exists(filepath.Join(main, d.Dir)):
			wi.MissingDeps = append(wi.MissingDeps, d.Dir)
		}
	}
}

func shortHash(h string) string {
	if len(h) > 9 {
		return h[:9]
	}
	return h
}

// ownDirty desconta do Dirty as deps configuradas que são link para outra
// pasta (node_modules -> main): no Linux o git vê o symlink como arquivo não
// versionado mesmo com "node_modules/" no .gitignore, e a worktree pareceria suja.
func ownDirty(r *RepoConfig, path string, st wtStatus) int {
	n := st.Dirty
	for _, d := range r.Deps {
		dir := strings.TrimSuffix(filepath.ToSlash(d.Dir), "/")
		if slices.Contains(st.Untracked, dir) && isLink(filepath.Join(path, filepath.FromSlash(dir))) {
			n--
		}
	}
	return n
}
