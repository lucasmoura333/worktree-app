package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// apiCall manda uma requisição pelo roteador completo (guard incluído).
func apiCall(t *testing.T, s *server, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	req.Host = "127.0.0.1:7777"
	if method != http.MethodGet {
		req.Header.Set("X-WT", "1")
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, req)
	return w
}

// ndjsonDone lê o fluxo NDJSON de uma operação e devolve o evento final
// e as mensagens de erro emitidas no caminho.
func ndjsonDone(t *testing.T, w *httptest.ResponseRecorder) (ok bool, errs []string) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("content-type = %q", ct)
	}
	sc := bufio.NewScanner(strings.NewReader(w.Body.String()))
	var last *event
	for sc.Scan() {
		var ev event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("linha NDJSON inválida %q: %v", sc.Text(), err)
		}
		if ev.Level == "error" {
			errs = append(errs, ev.Msg)
		}
		last = &ev
	}
	if last == nil || last.Level != "done" || last.OK == nil {
		t.Fatalf("fluxo sem evento done: %s", w.Body.String())
	}
	return *last.OK, errs
}

func jsonBody(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func listSnapshotsHTTP(t *testing.T, s *server, q url.Values) []SnapshotMeta {
	t.Helper()
	w := apiCall(t, s, http.MethodGet, "/api/snapshots?"+q.Encode(), "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/snapshots: %d %s", w.Code, w.Body.String())
	}
	var list []SnapshotMeta
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	return list
}

func TestSnapshotHTTPCreateListRestore(t *testing.T) {
	cfg := fixture(t)
	cfg.StateDir = t.TempDir()
	wt := createForSnapshot(t, cfg, "snaphttp")
	s := newServer(cfg)

	write(t, filepath.Join(wt.Path, "yarn.lock"), "lock-http\n")
	write(t, filepath.Join(wt.Path, "novo.txt"), "via http\n")

	// Criar.
	ok, errs := ndjsonDone(t, apiCall(t, s, http.MethodPost, "/api/snapshot",
		jsonBody(t, map[string]string{"repo": "app", "path": wt.Path, "note": "antes do teste", "reason": "manual"})))
	if !ok {
		t.Fatalf("snapshot falhou: %v", errs)
	}

	// Listar: por repo+path, por repo e geral.
	q := url.Values{"repo": {"app"}, "path": {wt.Path}}
	list := listSnapshotsHTTP(t, s, q)
	if len(list) != 1 {
		t.Fatalf("esperava 1 snapshot, veio %d", len(list))
	}
	meta := list[0]
	if meta.Note != "antes do teste" || meta.Reason != "manual" || !meta.HasPatch || !meta.HasUntracked || meta.Dir == "" {
		t.Fatalf("meta inesperada: %+v", meta)
	}
	if !hasName(meta.Omitted, ".env") {
		t.Fatalf(".env deveria constar como omitido: %v", meta.Omitted)
	}
	if n := len(listSnapshotsHTTP(t, s, url.Values{"repo": {"app"}})); n != 1 {
		t.Fatalf("lista por repo = %d", n)
	}
	if n := len(listSnapshotsHTTP(t, s, url.Values{})); n != 1 {
		t.Fatalf("lista geral = %d", n)
	}
	if n := len(listSnapshotsHTTP(t, s, url.Values{"repo": {"app"}, "path": {filepath.Join(cfg.Root, "outra-pasta")}})); n != 0 {
		t.Fatalf("lista de outra pasta = %d", n)
	}

	name := filepath.Base(meta.Dir)
	restore := func() (bool, []string) {
		return ndjsonDone(t, apiCall(t, s, http.MethodPost, "/api/snapshot/restore",
			jsonBody(t, map[string]string{"repo": "app", "path": wt.Path, "snapshot": name})))
	}

	// Worktree suja: a restauração é recusada e nada muda.
	ok, errs = restore()
	if ok || len(errs) == 0 || !strings.Contains(errs[0], "alterações") {
		t.Fatalf("restauração em worktree suja deveria ser recusada: ok=%v errs=%v", ok, errs)
	}
	if got := read(t, filepath.Join(wt.Path, "yarn.lock")); got != "lock-http\n" {
		t.Fatalf("recusa alterou yarn.lock: %q", got)
	}

	// Limpa e restaura.
	run(t, wt.Path, "git", "checkout", "--", "yarn.lock")
	if err := os.Remove(filepath.Join(wt.Path, "novo.txt")); err != nil {
		t.Fatal(err)
	}
	ok, errs = restore()
	if !ok {
		t.Fatalf("restauração falhou: %v", errs)
	}
	if got := read(t, filepath.Join(wt.Path, "yarn.lock")); got != "lock-http\n" {
		t.Fatalf("yarn.lock restaurado = %q", got)
	}
	if got := read(t, filepath.Join(wt.Path, "novo.txt")); got != "via http\n" {
		t.Fatalf("novo.txt restaurado = %q", got)
	}
}

func TestSnapshotHTTPRejectsBadInput(t *testing.T) {
	cfg := fixture(t)
	cfg.StateDir = t.TempDir()
	wt := createForSnapshot(t, cfg, "snapbad")
	s := newServer(cfg)

	// GET /api/snapshots: validação de parâmetros.
	for _, tc := range []struct {
		q    url.Values
		code int
	}{
		{url.Values{"repo": {"nao-existe"}}, http.StatusBadRequest},
		{url.Values{"path": {wt.Path}}, http.StatusBadRequest},
	} {
		if w := apiCall(t, s, http.MethodGet, "/api/snapshots?"+tc.q.Encode(), ""); w.Code != tc.code {
			t.Fatalf("%v: status %d, queria %d", tc.q, w.Code, tc.code)
		}
	}

	// POST /api/snapshot: repo desconhecido, caminho fora do root e campo extra.
	for _, body := range []string{
		jsonBody(t, map[string]string{"repo": "nao-existe", "path": wt.Path}),
		jsonBody(t, map[string]string{"repo": "app", "path": t.TempDir()}),
		`{"repo":"app","path":"` + filepath.ToSlash(wt.Path) + `","extra":1}`,
	} {
		if ok, _ := ndjsonDone(t, apiCall(t, s, http.MethodPost, "/api/snapshot", body)); ok {
			t.Fatalf("snapshot deveria falhar: %s", body)
		}
	}

	// POST /api/snapshot/restore: nomes que tentam sair da pasta ou não existem.
	for _, name := range []string{"", ".", "..", "../x", `a\b`, "nao-existe"} {
		body := jsonBody(t, map[string]string{"repo": "app", "path": wt.Path, "snapshot": name})
		ok, errs := ndjsonDone(t, apiCall(t, s, http.MethodPost, "/api/snapshot/restore", body))
		if ok {
			t.Fatalf("restore %q deveria falhar", name)
		}
		if len(errs) == 0 || !strings.Contains(errs[0], "snapshot") {
			t.Fatalf("restore %q: erro inesperado %v", name, errs)
		}
	}

	// Sem X-WT o guard barra antes de chegar na operação.
	req := httptest.NewRequest(http.MethodPost, "/api/snapshot", strings.NewReader(jsonBody(t, map[string]string{"repo": "app", "path": wt.Path})))
	req.Host = "127.0.0.1:7777"
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("sem X-WT: %d", w.Code)
	}
	if n := len(listSnapshotsHTTP(t, s, url.Values{})); n != 0 {
		t.Fatalf("nenhum snapshot deveria existir, há %d", n)
	}
}

func readOps(t *testing.T, cfg *Config) []opRecord {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cfg.StateDir, opsFile))
	if err != nil {
		t.Fatal(err)
	}
	var out []opRecord
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var rec opRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("linha inválida %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func TestOpsHistoryRecordsEachOperation(t *testing.T) {
	cfg := fixture(t)
	cfg.StateDir = t.TempDir()
	wt := createForSnapshot(t, cfg, "opslog")
	s := newServer(cfg)

	if ok, errs := ndjsonDone(t, apiCall(t, s, http.MethodPost, "/api/snapshot",
		jsonBody(t, map[string]string{"repo": "app", "path": wt.Path, "note": "hist"}))); !ok {
		t.Fatalf("snapshot falhou: %v", errs)
	}
	if ok, _ := ndjsonDone(t, apiCall(t, s, http.MethodPost, "/api/snapshot/restore",
		jsonBody(t, map[string]string{"repo": "app", "path": wt.Path, "snapshot": "nao-existe"}))); ok {
		t.Fatal("restore de snapshot inexistente deveria falhar")
	}

	ops := readOps(t, cfg)
	if len(ops) != 2 {
		t.Fatalf("esperava 2 operações, veio %d: %+v", len(ops), ops)
	}
	snap, rest := ops[0], ops[1]
	if snap.Op != "snapshot" || !snap.OK || snap.Error != "" || snap.ID == "" || snap.Start.IsZero() {
		t.Fatalf("registro do snapshot: %+v", snap)
	}
	var req map[string]string
	if err := json.Unmarshal(snap.Req, &req); err != nil || req["path"] != wt.Path || req["note"] != "hist" {
		t.Fatalf("pedido não guardado: %s (%v)", snap.Req, err)
	}
	if len(snap.Log) == 0 {
		t.Fatal("snapshot sem eventos no histórico")
	}
	if rest.Op != "snapshot/restore" || rest.OK || rest.Error == "" || rest.ID == snap.ID {
		t.Fatalf("registro do restore: %+v", rest)
	}
}

func TestOpsHistoryRotates(t *testing.T) {
	cfg := &Config{StateDir: t.TempDir()}
	p := filepath.Join(cfg.StateDir, opsFile)
	write(t, p, strings.Repeat("x", opsMaxBytes+1))
	if err := appendOp(cfg, opRecord{ID: "a", Op: "fetch", OK: true}); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(p + ".1"); err != nil || st.Size() <= opsMaxBytes {
		t.Fatalf("arquivo antigo não foi para .1: %v", err)
	}
	if ops := readOps(t, cfg); len(ops) != 1 || ops[0].ID != "a" {
		t.Fatalf("arquivo novo: %+v", ops)
	}
}

func TestOpRequestBodyKeepsBodyForHandler(t *testing.T) {
	big := `{"note":"` + strings.Repeat("a", opsMaxReq) + `"}`
	for body, keep := range map[string]bool{`{"repo": "app"}`: true, "não é json": false, big: false} {
		r := httptest.NewRequest(http.MethodPost, "/api/fetch", strings.NewReader(body))
		got := opRequestBody(r)
		if (got != nil) != keep {
			t.Errorf("%.20q: guardou=%v, quer %v", body, got != nil, keep)
		}
		if rest, _ := io.ReadAll(r.Body); string(rest) != body {
			t.Errorf("%.20q: corpo não voltou para o handler", body)
		}
	}
}
