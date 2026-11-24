package main

// Leitura do estado dos agentes: junta os registros gravados pelo subcomando
// "hook" com uma varredura de /proc, e liga cada sessão à worktree pelo cwd.
// Não roda git status nem toca o Vault: é barato para a tela consultar a
// cada poucos segundos.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// AgentInfo é uma sessão de agente ligada a uma worktree.
type AgentInfo struct {
	AgentRecord
	Repo    string `json:"repo"`
	Feature string `json:"feature,omitempty"`
	Folder  string `json:"folder"`
	Path    string `json:"path"`
	Main    bool   `json:"main,omitempty"`
	Alive   bool   `json:"alive"`
	// NoHook: processo de agente achado em /proc sem registro de hook.
	NoHook bool `json:"noHook,omitempty"`
}

type AgentCounts struct {
	Working int `json:"working"`
	Waiting int `json:"waiting"`
	Done    int `json:"done"`
	Running int `json:"running"`
}

type AgentsState struct {
	Agents []AgentInfo `json:"agents"`
	Counts AgentCounts `json:"counts"`
	Dir    string      `json:"dir"`
}

// wtEntry é o mínimo de uma worktree para ligar cwd → card.
type wtEntry struct {
	Repo, Feature, Folder, Path string
	Main                        bool
}

// wtIndex guarda a lista de worktrees por alguns segundos; só usa
// "git worktree list", que não lê o index nem cria lock.
type wtIndex struct {
	mu      sync.Mutex
	at      time.Time
	entries []wtEntry
}

const wtIndexTTL = 10 * time.Second

func (x *wtIndex) get(ctx context.Context, cfg *Config) []wtEntry {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.entries != nil && time.Since(x.at) < wtIndexTTL {
		return x.entries
	}
	x.entries = buildWtIndex(ctx, cfg)
	x.at = time.Now()
	return x.entries
}

func (x *wtIndex) invalidate() {
	x.mu.Lock()
	x.entries = nil
	x.mu.Unlock()
}

func buildWtIndex(ctx context.Context, cfg *Config) []wtEntry {
	out := []wtEntry{}
	for i := range cfg.Repos {
		r := &cfg.Repos[i]
		main := cfg.mainPath(r)
		if !isDir(main) {
			continue
		}
		list, err := listWorktrees(ctx, main)
		if err != nil {
			continue
		}
		for _, g := range list {
			if g.Bare || g.Prunable || !isDir(g.Path) {
				continue
			}
			folder := filepath.Base(g.Path)
			e := wtEntry{Repo: r.Key, Folder: folder, Path: g.Path}
			if samePath(g.Path, main) {
				e.Main = true
			} else {
				e.Feature = cfg.featureFromFolder(r, folder)
			}
			out = append(out, e)
		}
	}
	return out
}

// matchWorktree devolve a worktree mais funda que contém cwd. within() usa
// filepath.Rel, então gdpro-app não casa com gdpro-app-x.
func matchWorktree(entries []wtEntry, cwd string) (wtEntry, bool) {
	best, found := wtEntry{}, false
	for _, e := range entries {
		if within(e.Path, cwd) && (!found || len(e.Path) > len(best.Path)) {
			best, found = e, true
		}
	}
	return best, found
}

func loadAgentRecords(dir string, now time.Time) []AgentRecord {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []AgentRecord
	for _, e := range ents {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" || e.Name()[0] == '.' {
			continue
		}
		r, ok := readAgentRecord(filepath.Join(dir, e.Name()))
		if !ok || now.Sub(r.TS) > agentRecordMaxAge {
			continue
		}
		out = append(out, r)
	}
	return out
}

type procAgent struct {
	PID   int
	Agent string
	Cwd   string
}

// scanProcAgents lista processos de agente vivos e o cwd de cada um. Em
// sistemas sem /proc devolve vazio.
func scanProcAgents() []procAgent {
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	var out []procAgent
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 {
			continue
		}
		for a := range knownAgents {
			if !agentComm(pid, a) {
				continue
			}
			// Filhos do agente (o node do Claude, o musl do Codex) repetem o
			// mesmo comm; fica só o processo mais alto.
			if agentComm(parentPID(pid), a) {
				break
			}
			cwd, err := os.Readlink(filepath.Join(procRoot, e.Name(), "cwd"))
			if err == nil && filepath.IsAbs(cwd) {
				out = append(out, procAgent{PID: pid, Agent: a, Cwd: filepath.Clean(cwd)})
			}
			break
		}
	}
	return out
}

func collectAgents(entries []wtEntry, recs []AgentRecord, procs []procAgent, dir string) AgentsState {
	st := AgentsState{Agents: []AgentInfo{}, Dir: dir}
	claimed := map[int]bool{}
	// Mais recente primeiro: um /clear no Claude abre sessão nova no mesmo
	// processo, e só a nova fica viva.
	recs = append([]AgentRecord(nil), recs...)
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].TS.After(recs[j].TS) })
	for _, r := range recs {
		e, ok := matchWorktree(entries, r.Cwd)
		if !ok {
			continue // fora das worktrees conhecidas: não aparece
		}
		ai := AgentInfo{AgentRecord: r, Repo: e.Repo, Feature: e.Feature, Folder: e.Folder, Path: e.Path, Main: e.Main}
		if r.PID > 0 {
			ai.Alive = !claimed[r.PID] && agentComm(r.PID, r.Agent)
		} else {
			// Sem pid conhecido: vale qualquer processo do mesmo agente na mesma worktree.
			for _, p := range procs {
				if p.Agent == r.Agent && !claimed[p.PID] {
					if pe, ok := matchWorktree(entries, p.Cwd); ok && samePath(pe.Path, e.Path) {
						ai.Alive, ai.PID = true, p.PID
						break
					}
				}
			}
		}
		if ai.Alive {
			claimed[ai.PID] = true
		} else if ai.State != agentEnded {
			ai.State = agentEnded // processo morreu sem SessionEnd
		}
		st.Agents = append(st.Agents, ai)
	}
	for _, p := range procs {
		if claimed[p.PID] {
			continue
		}
		e, ok := matchWorktree(entries, p.Cwd)
		if !ok {
			continue
		}
		st.Agents = append(st.Agents, AgentInfo{
			AgentRecord: AgentRecord{Agent: p.Agent, Cwd: p.Cwd, PID: p.PID, State: "running"},
			Repo:        e.Repo, Feature: e.Feature, Folder: e.Folder, Path: e.Path, Main: e.Main, Alive: true, NoHook: true,
		})
	}
	for _, a := range st.Agents {
		if !a.Alive {
			continue
		}
		switch a.State {
		case agentWorking:
			st.Counts.Working++
		case agentWaiting:
			st.Counts.Waiting++
		case agentDone:
			st.Counts.Done++
		default:
			st.Counts.Running++
		}
	}
	// vivos primeiro; depois o mais recente
	sort.SliceStable(st.Agents, func(i, j int) bool {
		a, b := st.Agents[i], st.Agents[j]
		if a.Alive != b.Alive {
			return a.Alive
		}
		return a.TS.After(b.TS)
	})
	return st
}

func (s *server) agentsDir() string { return filepath.Join(s.cfg.StateDir, "agents") }

func (s *server) agentsState(ctx context.Context) AgentsState {
	dir := s.agentsDir()
	return collectAgents(s.wts.get(ctx, s.cfg), loadAgentRecords(dir, time.Now()), scanProcAgents(), dir)
}

func (s *server) handleAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.agentsState(r.Context()))
}
