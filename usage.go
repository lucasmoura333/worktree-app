package main

// Consumo por sessão: lê apenas os campos numéricos que o Claude Code e o
// Codex gravam nos seus transcripts (tokens e custo, quando houver). Prompt,
// resposta e saída de ferramenta nunca são guardados nem devolvidos.
//
// A leitura é incremental e fica em memória: o poll de /api/agents a cada 3 s
// não relê um transcript inteiro enquanto o agente trabalha. O opencode é
// consultado pelo próprio CLI (`opencode db`, só números, com cache curto) e o
// agy guarda a sessão num blob protobuf; para ele o card mostra "—".

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AgentUsage é o consumo numérico de uma sessão. Os nomes seguem o /api/agents.
type AgentUsage struct {
	Source     string  `json:"source,omitempty"`
	In         int64   `json:"in"`
	Out        int64   `json:"out"`
	CacheRead  int64   `json:"cacheRead,omitempty"`
	CacheWrite int64   `json:"cacheWrite,omitempty"`
	Reasoning  int64   `json:"reasoning,omitempty"`
	CostUSD    float64 `json:"costUsd,omitempty"`
	HasCost    bool    `json:"hasCost,omitempty"`
}

type usageFile struct {
	mod   time.Time
	off   int64
	usage AgentUsage
	seen  map[string]bool // ids de mensagem já contados (Claude streaming repete)
}

type resolvedPath struct {
	path string
	at   time.Time
}

type ocUsage struct {
	usage AgentUsage
	ok    bool
	at    time.Time
}

type usageReader struct {
	mu        sync.Mutex
	files     map[string]*usageFile
	paths     map[string]resolvedPath
	oc        map[string]ocUsage
	ocBin     string
	ocChecked bool
}

const (
	codexPathTTL     = 10 * time.Minute
	opencodeUsageTTL = 15 * time.Second
	opencodeTimeout  = 5 * time.Second
)

// opencodeDB é substituído nos testes. O opencode já traz o SQLite: roda a
// consulta só numérica com "--pure" (sem plugins) e devolve JSON.
var opencodeDB = func(ctx context.Context, bin, query string) ([]byte, error) {
	return exec.CommandContext(ctx, bin, "--pure", "db", query, "--format", "json").Output()
}

func (u *usageReader) initLocked() {
	if u.files == nil {
		u.files = map[string]*usageFile{}
		u.paths = map[string]resolvedPath{}
	}
	if u.oc == nil {
		u.oc = map[string]ocUsage{}
	}
}

// fill anexa o consumo às sessões vivas. Deve rodar com o estado já montado:
// só entram agentes com processo vivo e sessão conhecida. O lock fica só em
// volta dos mapas; a consulta ao opencode roda fora dele.
func (u *usageReader) fill(ctx context.Context, agents []AgentInfo) {
	for i := range agents {
		a := &agents[i]
		if !a.Alive || a.Session == "" {
			continue
		}
		var us AgentUsage
		var ok bool
		switch a.Agent {
		case "claude", "codex":
			u.mu.Lock()
			u.initLocked()
			if a.Agent == "claude" {
				us, ok = u.readLocked("claude", claudeUsagePath(a.Cwd, a.Session))
			} else {
				us, ok = u.readLocked("codex", u.codexPathLocked(a.Session))
			}
			u.mu.Unlock()
		case "opencode":
			us, ok = u.opencodeUsage(ctx, a.Session)
		default:
			continue
		}
		if ok {
			cp := us
			a.Usage = &cp
		}
	}
}

func (u *usageReader) opencodeUsage(ctx context.Context, session string) (AgentUsage, bool) {
	u.mu.Lock()
	u.initLocked()
	if c, ok := u.oc[session]; ok && time.Since(c.at) < opencodeUsageTTL {
		u.mu.Unlock()
		return c.usage, c.ok
	}
	if !u.ocChecked {
		u.ocBin, _ = exec.LookPath("opencode")
		u.ocChecked = true
	}
	bin := u.ocBin
	u.mu.Unlock()
	if bin == "" {
		return AgentUsage{}, false
	}
	qctx, cancel := context.WithTimeout(ctx, opencodeTimeout)
	defer cancel()
	q := fmt.Sprintf(`select count(*) as n, `+
		`coalesce(sum(json_extract(data,'$.tokens.input')),0) as tin, `+
		`coalesce(sum(json_extract(data,'$.tokens.output')),0) as tout, `+
		`coalesce(sum(json_extract(data,'$.tokens.reasoning')),0) as reason, `+
		`coalesce(sum(json_extract(data,'$.tokens.cache.read')),0) as cr, `+
		`coalesce(sum(json_extract(data,'$.tokens.cache.write')),0) as cw, `+
		`coalesce(sum(json_extract(data,'$.cost')),0) as cost `+
		`from message where session_id='%s' and json_extract(data,'$.role')='assistant'`, session)
	out, err := opencodeDB(qctx, bin, q)
	var rows []struct {
		N         int64   `json:"n"`
		In        int64   `json:"tin"`
		Out       int64   `json:"tout"`
		Reasoning int64   `json:"reason"`
		CacheRead int64   `json:"cr"`
		CacheW    int64   `json:"cw"`
		Cost      float64 `json:"cost"`
	}
	if err == nil {
		err = json.Unmarshal(out, &rows)
	}
	us := AgentUsage{Source: "opencode", HasCost: true}
	ok := err == nil && len(rows) > 0 && rows[0].N > 0
	if ok {
		us.In = rows[0].In
		us.Out = rows[0].Out
		us.Reasoning = rows[0].Reasoning
		us.CacheRead = rows[0].CacheRead
		us.CacheWrite = rows[0].CacheW
		us.CostUSD = rows[0].Cost
	}
	u.mu.Lock()
	u.oc[session] = ocUsage{usage: us, ok: ok, at: time.Now()}
	u.mu.Unlock()
	return us, ok
}

// claudeUsagePath reproduz o nome que o Claude usa para a pasta do projeto:
// "/" e "." viram "-" (/home/x/a.b -> -home-x-a-b).
func claudeUsagePath(cwd, session string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := strings.NewReplacer("/", "-", "\\", "-", ".", "-").Replace(cwd)
	return filepath.Join(home, ".claude", "projects", dir, session+".jsonl")
}

func (u *usageReader) codexPathLocked(session string) string {
	if p, ok := u.paths[session]; ok && time.Since(p.at) < codexPathTTL {
		return p.path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	base := filepath.Join(home, ".codex", "sessions")
	matches, _ := filepath.Glob(filepath.Join(base, "*", "*", "*", "rollout-*-"+session+".jsonl"))
	best, mod := "", time.Time{}
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.ModTime().After(mod) {
			best, mod = m, fi.ModTime()
		}
	}
	if best != "" {
		u.paths[session] = resolvedPath{path: best, at: time.Now()}
	}
	return best
}

// readLocked relê só o que cresceu desde a chamada anterior. Se o arquivo
// encolheu (rotação/truncamento), recomeça do zero.
func (u *usageReader) readLocked(agent, path string) (AgentUsage, bool) {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		delete(u.files, path)
		return AgentUsage{}, false
	}
	st := u.files[path]
	if st == nil || fi.Size() < st.off {
		st = &usageFile{seen: map[string]bool{}}
		u.files[path] = st
	}
	if fi.Size() == st.off && fi.ModTime().Equal(st.mod) {
		return st.usage, st.usage.Source != ""
	}
	f, err := os.Open(path)
	if err != nil {
		return AgentUsage{}, false
	}
	defer f.Close()
	if _, err := f.Seek(st.off, io.SeekStart); err != nil {
		return AgentUsage{}, false
	}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		n := len(line)
		if n > 0 {
			// Linha completa termina em \n. No fim do arquivo sem \n, só
			// consome se o JSON já estiver inteiro (não perder um token_count
			// que chegou sem quebra de linha).
			if line[n-1] == '\n' || json.Valid(line) {
				st.parse(agent, line)
				st.off += int64(n)
			}
		}
		if err != nil {
			break
		}
	}
	st.mod = fi.ModTime()
	return st.usage, st.usage.Source != ""
}

func (st *usageFile) parse(agent string, line []byte) {
	switch agent {
	case "claude":
		var e struct {
			Type    string `json:"type"`
			Message struct {
				ID    string `json:"id"`
				Usage *struct {
					InputTokens        int64 `json:"input_tokens"`
					OutputTokens       int64 `json:"output_tokens"`
					CacheReadTokens    int64 `json:"cache_read_input_tokens"`
					CacheCreationToken int64 `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &e) != nil || e.Type != "assistant" || e.Message.Usage == nil {
			return
		}
		if id := e.Message.ID; id != "" {
			if st.seen[id] {
				return
			}
			st.seen[id] = true
		}
		st.usage.Source = "claude-code"
		st.usage.In += e.Message.Usage.InputTokens
		st.usage.Out += e.Message.Usage.OutputTokens
		st.usage.CacheRead += e.Message.Usage.CacheReadTokens
		st.usage.CacheWrite += e.Message.Usage.CacheCreationToken
	case "codex":
		var e struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
				Info *struct {
					Total *struct {
						InputTokens       int64 `json:"input_tokens"`
						CachedInputTokens int64 `json:"cached_input_tokens"`
						CacheWriteTokens  int64 `json:"cache_write_input_tokens"`
						OutputTokens      int64 `json:"output_tokens"`
						ReasoningTokens   int64 `json:"reasoning_output_tokens"`
					} `json:"total_token_usage"`
				} `json:"info"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &e) != nil || e.Payload.Type != "token_count" || e.Payload.Info == nil || e.Payload.Info.Total == nil {
			return
		}
		t := e.Payload.Info.Total
		in := t.InputTokens - t.CachedInputTokens
		if in < 0 {
			in = 0
		}
		st.usage.Source = "codex"
		st.usage.In = in
		st.usage.Out = t.OutputTokens
		st.usage.CacheRead = t.CachedInputTokens
		st.usage.CacheWrite = t.CacheWriteTokens
		st.usage.Reasoning = t.ReasoningTokens
	}
}
