package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Histórico das operações em StateDir/ops.jsonl: uma linha por operação, para
// saber depois da tela fechada o que rodou, com que pedido e onde parou.
const (
	opsFile     = "ops.jsonl"
	opsMaxBytes = 2 << 20 // acima disso o arquivo vira ops.jsonl.1
	opsKeepLog  = 20
	opsMaxReq   = 8 << 10
)

type opRecord struct {
	ID    string          `json:"id"`
	Op    string          `json:"op"`
	Start time.Time       `json:"start"`
	Ms    int64           `json:"ms"`
	Req   json.RawMessage `json:"req,omitempty"`
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Log   []event         `json:"log,omitempty"`
}

// opTrail repassa os eventos e guarda os últimos, que mostram a etapa da falha.
type opTrail struct {
	next Logger
	mu   sync.Mutex
	last []event
}

func (t *opTrail) Log(repo, level, msg string) {
	t.next.Log(repo, level, msg)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.last = append(t.last, event{T: time.Now().Format("15:04:05"), Repo: repo, Level: level, Msg: msg})
	if len(t.last) > opsKeepLog {
		t.last = t.last[len(t.last)-opsKeepLog:]
	}
}

func (t *opTrail) events() []event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]event(nil), t.last...)
}

// opRequestBody lê o corpo e o devolve à requisição; só guarda JSON pequeno.
func opRequestBody(r *http.Request) json.RawMessage {
	if r.Body == nil {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	r.Body = io.NopCloser(bytes.NewReader(b))
	var out bytes.Buffer
	if len(b) > opsMaxReq || json.Compact(&out, b) != nil {
		return nil
	}
	return out.Bytes()
}

func newOpID(t time.Time) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return t.Format("20060102T150405.000") + "-" + hex.EncodeToString(b[:])
}

func appendOp(c *Config, rec opRecord) error {
	dir := stateRoot(c)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	p := filepath.Join(dir, opsFile)
	if st, err := os.Stat(p); err == nil && st.Size() > opsMaxBytes {
		_ = os.Rename(p, p+".1")
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	return errors.Join(werr, f.Close())
}

func (s *server) recordOp(path string, start time.Time, req json.RawMessage, trail *opTrail, err error, lg Logger) {
	rec := opRecord{
		ID:    newOpID(start),
		Op:    strings.TrimPrefix(path, "/api/"),
		Start: start,
		Ms:    time.Since(start).Milliseconds(),
		Req:   req,
		OK:    err == nil,
		Log:   trail.events(),
	}
	if err != nil {
		rec.Error = err.Error()
	}
	if werr := appendOp(s.cfg, rec); werr != nil {
		lg.Log("", "warn", "histórico de operações: "+werr.Error())
	}
}
