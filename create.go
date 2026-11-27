package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type CreateRepoReq struct {
	Key    string `json:"key"`
	Mode   string `json:"mode"`
	Base   string `json:"base"`
	Branch string `json:"branch"`
}

type CreateReq struct {
	Feature   string          `json:"feature"`
	Repos     []CreateRepoReq `json:"repos"`
	Install   bool            `json:"install"`
	Fetch     bool            `json:"fetch"`
	Tasks     bool            `json:"tasks"`
	Workspace bool            `json:"workspace"`
}

type PlanItem struct {
	Repo       string   `json:"repo"`
	Mode       string   `json:"mode"`
	Path       string   `json:"path"`
	Folder     string   `json:"folder"`
	Branch     string   `json:"branch"`
	StartPoint string   `json:"startPoint"`
	NewBranch  bool     `json:"newBranch"`
	Track      bool     `json:"track"`
	Port       int      `json:"port,omitempty"`
	Problems   []string `json:"problems"`
}

type Plan struct {
	Feature         string     `json:"feature"`
	Items           []PlanItem `json:"items"`
	Workspace       string     `json:"workspace"`
	WorkspaceExists bool       `json:"workspaceExists"`
	TasksDir        string     `json:"tasksDir"`
	TasksExists     bool       `json:"tasksExists"`
	Problems        []string   `json:"problems"`
	OK              bool       `json:"ok"`
}

var featureRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func validFeature(name string) error {
	if !featureRe.MatchString(name) || strings.Contains(name, "..") || strings.HasSuffix(name, ".") ||
		strings.HasSuffix(strings.ToLower(name), ".lock") {
		return errors.New("nome inválido: use letras, números, '.', '_' ou '-' (até 64)")
	}
	return nil
}

// validRef barra o que o git leria como opção ou que quebraria a linha de comando.
func validRef(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.ContainsAny(s, " ~^:?*[\\") {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return false
		}
	}
	return true
}

func buildPlan(ctx context.Context, cfg *Config, req *CreateReq, ports *portAllocator, reserve bool) *Plan {
	p := &Plan{Feature: req.Feature, Items: []PlanItem{}, Problems: []string{}}
	if err := validFeature(req.Feature); err != nil {
		p.Problems = append(p.Problems, err.Error())
		return p
	}
	if len(req.Repos) == 0 {
		p.Problems = append(p.Problems, "marque ao menos um repo")
	}
	now := time.Now()
	seen := map[string]bool{}
	for _, rr := range req.Repos {
		it := PlanItem{Repo: rr.Key, Mode: rr.Mode, Problems: []string{}}
		r := cfg.repo(rr.Key)
		if r == nil || seen[rr.Key] {
			it.Problems = append(it.Problems, "repo desconhecido ou repetido")
			p.Items = append(p.Items, it)
			continue
		}
		seen[rr.Key] = true
		planRepo(ctx, cfg, r, req.Feature, rr, &it, now)
		if r.DockerEnv != nil && len(it.Problems) == 0 {
			if port, err := ports.next(cfg, r, reserve); err == nil {
				it.Port = port
			} else {
				it.Problems = append(it.Problems, err.Error())
			}
		}
		p.Items = append(p.Items, it)
	}
	ws := cfg.workspacePath(req.Feature)
	p.Workspace, p.WorkspaceExists = filepath.Base(ws), exists(ws)
	td := cfg.tasksPath(req.Feature)
	p.TasksDir, p.TasksExists = relSlash(cfg.Root, td), isDir(td)

	p.OK = len(p.Problems) == 0
	for _, it := range p.Items {
		if len(it.Problems) > 0 {
			p.OK = false
		}
	}
	return p
}

func planRepo(ctx context.Context, cfg *Config, r *RepoConfig, feature string, rr CreateRepoReq, it *PlanItem, now time.Time) {
	main := cfg.mainPath(r)
	problem := func(f string, a ...any) { it.Problems = append(it.Problems, fmt.Sprintf(f, a...)) }
	it.Folder = cfg.folderName(r, feature)
	it.Path = filepath.Join(cfg.containerPath(r), it.Folder)
	if exists(it.Path) {
		problem("a pasta %s já existe", relSlash(cfg.Root, it.Path))
	}
	// Worktree dentro do repo principal vira pasta untracked no main e entra no
	// que Vite, IDE e ferramentas varrem. Ajuste worktreeRoot/folderPattern.
	if within(main, it.Path) {
		problem("a worktree %s ficaria dentro do repo principal; configure worktreeRoot", relSlash(cfg.Root, it.Path))
	}
	wts, err := listWorktrees(ctx, main)
	if err != nil {
		problem("%v", err)
		return
	}
	inUse := map[string]string{}
	for _, w := range wts {
		if w.Branch != "" {
			inUse[w.Branch] = w.Path
		}
	}
	switch rr.Mode {
	case "", "new":
		it.Mode = "new"
		base := strings.TrimSpace(rr.Base)
		if base == "" {
			base = r.Base
		}
		it.Branch, it.StartPoint, it.NewBranch = cfg.branchName(feature, now), base, true
		switch {
		case !branchRefOK(ctx, main, it.Branch):
			problem("a branch %s não é um nome válido para o git; mude a feature", it.Branch)
		case !validRef(base):
			problem("origem inválida")
		case !refExists(ctx, main, base+"^{commit}"):
			problem("origem %s não encontrada (rode o fetch)", base)
		}
		if refExists(ctx, main, "refs/heads/"+it.Branch) {
			problem("a branch %s já existe; use \"branch existente\"", it.Branch)
		}
	case "existing":
		name := strings.TrimPrefix(strings.TrimSpace(rr.Branch), "origin/")
		if !validRef(name) {
			problem("informe uma branch válida")
			return
		}
		it.Branch = name
		switch {
		case refExists(ctx, main, "refs/heads/"+name):
		case refExists(ctx, main, "refs/remotes/origin/"+name):
			it.NewBranch, it.Track, it.StartPoint = true, true, "origin/"+name
		default:
			problem("branch %s não existe local nem em origin", name)
		}
		if p, ok := inUse[name]; ok {
			problem("a branch %s já está aberta em %s", name, relSlash(cfg.Root, p))
		}
	default:
		problem("modo inválido")
	}
}

func createFeature(ctx context.Context, cfg *Config, req *CreateReq, ports *portAllocator, lg Logger) error {
	ports.reset()
	p := buildPlan(ctx, cfg, req, ports, true)
	if !p.OK {
		for _, msg := range p.Problems {
			lg.Log("", "error", msg)
		}
		for _, it := range p.Items {
			for _, msg := range it.Problems {
				lg.Log(it.Repo, "error", msg)
			}
		}
		return errors.New("nada foi criado: corrija o que está no plano")
	}
	added := make([]bool, len(p.Items))
	errs := make([]error, len(p.Items))
	var wg sync.WaitGroup
	for i := range p.Items {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			added[i], errs[i] = createOne(ctx, cfg, req, &p.Items[i], ports, lg)
		}(i)
	}
	wg.Wait()

	var created []PlanItem
	for i, it := range p.Items {
		if added[i] {
			created = append(created, it)
		}
	}
	if len(created) > 0 {
		if req.Tasks {
			ensureTasks(cfg, req.Feature, created, lg)
		}
		if req.Workspace {
			ensureWorkspace(cfg, req.Feature, created, lg)
		}
	}
	return errors.Join(errs...)
}

// createOne devolve added=true quando a worktree existe no fim, mesmo que o
// setup tenha falhado (aí dá para completar depois pela tela).
func createOne(ctx context.Context, cfg *Config, req *CreateReq, it *PlanItem, ports *portAllocator, lg Logger) (bool, error) {
	r := cfg.repo(it.Repo)
	main := cfg.mainPath(r)
	if req.Fetch {
		lg.Log(r.Key, "info", "git fetch origin --prune")
		if _, err := git(ctx, main, "fetch", "origin", "--prune"); err != nil {
			lg.Log(r.Key, "error", err.Error())
			return false, fmt.Errorf("%s: %w", r.Key, err)
		}
	}
	args := []string{"worktree", "add"}
	if it.NewBranch {
		if it.Track {
			args = append(args, "--track")
		} else {
			// Sem isso a branch nova herdaria origin/dev como upstream.
			args = append(args, "--no-track")
		}
		args = append(args, "-b", it.Branch, it.Path, it.StartPoint)
	} else {
		args = append(args, it.Path, it.Branch)
	}
	start := time.Now()
	lg.Log(r.Key, "info", "git "+strings.Join(args, " "))
	if _, err := git(ctx, main, args...); err != nil {
		lg.Log(r.Key, "error", err.Error())
		return false, fmt.Errorf("%s: %w", r.Key, err)
	}
	lg.Log(r.Key, "ok", fmt.Sprintf("worktree %s na branch %s (%s)", relSlash(cfg.Root, it.Path), it.Branch, since(start)))
	if err := setupWorktree(ctx, cfg, r, req.Feature, it.Path, req.Install, it.Port, ports, lg); err != nil {
		return true, fmt.Errorf("%s: %w", r.Key, err)
	}
	return true, nil
}
