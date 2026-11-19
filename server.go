package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

type Logger interface {
	Log(repo, level, msg string)
}

type server struct {
	cfg     *Config
	opMu    sync.Mutex
	vaultMu sync.Mutex
	docs    docIndex
	graph   graphCache
	ports   *portAllocator
}

func newServer(cfg *Config) *server { return &server{cfg: cfg, ports: newPortAllocator()} }

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServerFS(sub))
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/branches", s.handleBranches)
	mux.HandleFunc("POST /api/plan", s.handlePlan)
	mux.HandleFunc("POST /api/connections", s.handleConnections)
	mux.HandleFunc("GET /api/doc", s.handleDoc)
	mux.HandleFunc("GET /api/asset", s.handleAsset)
	mux.HandleFunc("GET /api/graph", s.handleGraph)
	mux.HandleFunc("POST /api/open", s.handleOpen)
	mux.HandleFunc("POST /api/create", s.stream(s.opCreate))
	mux.HandleFunc("POST /api/setup", s.stream(s.opSetup))
	mux.HandleFunc("POST /api/remove", s.stream(s.opRemove))
	mux.HandleFunc("POST /api/prune", s.stream(s.opPrune))
	mux.HandleFunc("POST /api/fetch", s.stream(s.opFetch))
	return s.guard(mux)
}

// guard aceita só o próprio host (barra DNS rebinding) e exige o header X-WT
// nos POSTs: um site qualquer não consegue mandá-lo sem preflight CORS, que
// este servidor nunca libera.
func (s *server) guard(next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(s.cfg.Listen)
	allowed := map[string]bool{s.cfg.Listen: true, "127.0.0.1:" + port: true, "localhost:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[r.Host] {
			http.Error(w, "host não permitido", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("X-WT") != "1" {
			http.Error(w, "requisição recusada", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'self'")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	// Durante uma operação longa (instalar deps) a tela não pode travar:
	// devolve o estado lendo o Vault, sem gravar nada no meio da operação.
	if !s.opMu.TryLock() {
		st := scanState(r.Context(), s.cfg)
		s.peekVault(st)
		st.Busy = true
		writeJSON(w, st)
		return
	}
	defer s.opMu.Unlock()
	st := scanState(r.Context(), s.cfg)
	s.syncVault(st)
	writeJSON(w, st)
}

func (s *server) handleBranches(w http.ResponseWriter, r *http.Request) {
	rc := s.cfg.repo(r.URL.Query().Get("repo"))
	if rc == nil {
		http.Error(w, "repo desconhecido", http.StatusNotFound)
		return
	}
	list, err := listBranches(r.Context(), s.cfg.mainPath(rc))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []Branch{}
	}
	writeJSON(w, list)
}

func (s *server) handlePlan(w http.ResponseWriter, r *http.Request) {
	var req CreateReq
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, buildPlan(r.Context(), s.cfg, &req, s.ports, false))
}

var safePathRe = regexp.MustCompile(`^[A-Za-z0-9 ._\-:\\/]+$`)

// handleOpen abre um workspace no editor ou uma pasta no Explorer. Só aceita
// caminhos dentro do root e com caracteres comuns, porque o editor no Windows
// costuma ser um .cmd interpretado pelo cmd.exe.
func (s *server) handleOpen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		With string `json:"with"`
	}
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p := filepath.FromSlash(req.Path)
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.cfg.Root, p)
	}
	p = filepath.Clean(p)
	if !within(s.cfg.Root, p) || !exists(p) || !safePathRe.MatchString(p) {
		http.Error(w, "caminho não permitido", http.StatusBadRequest)
		return
	}
	var cmd *exec.Cmd
	if req.With == "explorer" {
		explorerPath := p
		if runtime.GOOS == "linux" {
			converted, err := exec.CommandContext(r.Context(), "wslpath", "-w", p).Output()
			if err != nil {
				http.Error(w, "não foi possível converter o caminho para Explorer", http.StatusInternalServerError)
				return
			}
			explorerPath = strings.TrimSpace(string(converted))
		}
		cmd = exec.Command("explorer.exe", explorerPath)
	} else {
		argv := append(append([]string{}, s.cfg.Editor...), p)
		cmd = exec.Command(argv[0], argv[1:]...)
		hideWindow(cmd)
	}
	if err := cmd.Start(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go func() { _ = cmd.Wait() }()
	writeJSON(w, map[string]bool{"ok": true})
}

type event struct {
	T     string `json:"t"`
	Repo  string `json:"repo,omitempty"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
	OK    *bool  `json:"ok,omitempty"`
}

// streamLog manda cada evento como uma linha JSON (NDJSON) e espelha no console.
type streamLog struct {
	mu sync.Mutex
	w  io.Writer
	fl http.Flusher
}

func (l *streamLog) Log(repo, level, msg string) { l.emit(event{Repo: repo, Level: level, Msg: msg}) }

func (l *streamLog) emit(ev event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	ev.T = time.Now().Format("15:04:05")
	b, _ := json.Marshal(ev)
	_, _ = l.w.Write(append(b, '\n'))
	if l.fl != nil {
		l.fl.Flush()
	}
	repo := ev.Repo
	if repo == "" {
		repo = "-"
	}
	fmt.Printf("%s %-5s %-12s %s\n", ev.T, ev.Level, repo, ev.Msg)
}

func (l *streamLog) done(err error) {
	ok := err == nil
	msg := "concluído"
	if !ok {
		l.Log("", "error", err.Error())
		msg = "terminou com erro"
	}
	l.emit(event{Level: "done", Msg: msg, OK: &ok})
}

type opFunc func(ctx context.Context, r *http.Request, lg Logger) error

// stream roda uma operação por vez. Ela segue até o fim mesmo se a aba
// fechar, para não deixar worktree pela metade.
func (s *server) stream(fn opFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.opMu.TryLock() {
			http.Error(w, "já existe uma operação em andamento", http.StatusConflict)
			return
		}
		defer s.opMu.Unlock()
		w.Header().Set("Content-Type", "application/x-ndjson")
		fl, _ := w.(http.Flusher)
		lg := &streamLog{w: w, fl: fl}
		ctx := context.WithoutCancel(r.Context())
		before := scanState(ctx, s.cfg)
		s.syncVault(before)
		// A removal must not lose its last SHA if the Vault cannot retain it.
		if r.URL.Path == "/api/remove" && before.VaultError != "" {
			lg.done(fmt.Errorf("Vault: %s; remoção cancelada", before.VaultError))
			return
		}
		err := fn(ctx, r, lg)
		after := scanState(ctx, s.cfg)
		s.syncVault(after)
		if after.VaultError != "" {
			lg.Log("", "warn", "Vault: "+after.VaultError)
		}
		lg.done(err)
	}
}

func (s *server) opCreate(ctx context.Context, r *http.Request, lg Logger) error {
	var req CreateReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	return createFeature(ctx, s.cfg, &req, s.ports, lg)
}

func (s *server) opSetup(ctx context.Context, r *http.Request, lg Logger) error {
	var req struct {
		Repo    string `json:"repo"`
		Path    string `json:"path"`
		Install bool   `json:"install"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	rc := s.cfg.repo(req.Repo)
	if rc == nil {
		return errors.New("repo desconhecido")
	}
	wts, err := listWorktrees(ctx, s.cfg.mainPath(rc))
	if err != nil {
		return err
	}
	for _, wt := range wts {
		if samePath(wt.Path, req.Path) && !samePath(wt.Path, s.cfg.mainPath(rc)) && !wt.Bare && isDir(wt.Path) {
			s.ports.reset()
			feature := s.cfg.featureFromFolder(rc, filepath.Base(wt.Path))
			return setupWorktree(ctx, s.cfg, rc, feature, wt.Path, req.Install, 0, s.ports, lg)
		}
	}
	return errors.New("worktree não encontrada nesse repo")
}

func (s *server) opRemove(ctx context.Context, r *http.Request, lg Logger) error {
	var req RemoveReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	return removeFeature(ctx, s.cfg, &req, lg)
}

type reposReq struct {
	Repos []string `json:"repos"`
}

func (s *server) opPrune(ctx context.Context, r *http.Request, lg Logger) error {
	var req reposReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	return pruneRepos(ctx, s.cfg, req.Repos, lg)
}

func (s *server) opFetch(ctx context.Context, r *http.Request, lg Logger) error {
	var req reposReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	return fetchRepos(ctx, s.cfg, req.Repos, lg)
}
