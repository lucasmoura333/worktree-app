package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Smoke do frontend sem navegador: a página sobe pelo roteador real, todo
// asset referenciado existe, os headers de segurança vêm junto, cada id que
// o app.js procura existe no index.html e os scripts passam no node --check.

var (
	assetRefRe = regexp.MustCompile(`(?:src|href)="([^"#:]+)"`)
	idRefRe    = regexp.MustCompile(`(?:\$|getElementById|querySelector)\(\s*"#?([A-Za-z][\w-]*)"\s*\)`)
	idDefRe    = regexp.MustCompile(`\sid="([^"]+)"`)
)

func TestWebSmoke(t *testing.T) {
	cfg := fixture(t)
	s := newServer(cfg)

	w := apiCall(t, s, http.MethodGet, "/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /: %d", w.Code)
	}
	html := w.Body.String()
	if !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("content-type = %q", w.Header().Get("Content-Type"))
	}
	for h, want := range map[string]string{
		"Content-Security-Policy": "default-src 'self'",
		"X-Content-Type-Options":  "nosniff",
		"Cache-Control":           "no-store",
	} {
		if got := w.Header().Get(h); got != want {
			t.Errorf("%s = %q, queria %q", h, got, want)
		}
	}
	// A CSP proíbe script inline: o index não pode depender disso.
	if regexp.MustCompile(`<script>[^<]*\S`).MatchString(html) || strings.Contains(html, " onclick=") {
		t.Error("index.html tem script inline, que a CSP bloqueia")
	}

	refs := assetRefRe.FindAllStringSubmatch(html, -1)
	if len(refs) < 4 {
		t.Fatalf("poucos assets no index: %v", refs)
	}
	for _, m := range refs {
		aw := apiCall(t, s, http.MethodGet, "/"+strings.TrimPrefix(m[1], "/"), "")
		if aw.Code != http.StatusOK || aw.Body.Len() == 0 {
			t.Errorf("asset %s: %d (%d bytes)", m[1], aw.Code, aw.Body.Len())
		}
	}

	// Host estranho é barrado mesmo na página estática (DNS rebinding).
	if w := hostCall(t, s, "evil.example:7777"); w.Code != http.StatusForbidden {
		t.Errorf("host externo: %d", w.Code)
	}

	// Ids que o JS procura precisam existir no HTML (ou ser criados pelo JS).
	defined := map[string]bool{}
	for _, m := range idDefRe.FindAllStringSubmatch(html, -1) {
		defined[m[1]] = true
	}
	var missing []string
	seen := 0
	for _, f := range []string{"app.js", "graph.js", "md.js"} {
		src, err := os.ReadFile(filepath.Join("web", f))
		if err != nil {
			t.Fatal(err)
		}
		js := string(src)
		for _, m := range idRefRe.FindAllStringSubmatch(js, -1) {
			id := m[1]
			seen++
			if defined[id] || strings.Contains(js, `id: "`+id+`"`) || strings.Contains(js, `.id = "`+id+`"`) {
				continue
			}
			missing = append(missing, f+": #"+id)
		}
	}
	if seen < 30 {
		t.Errorf("só %d referências de id encontradas no JS; o regex parou de casar?", seen)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("ids usados no JS e ausentes no index.html: %v", dedup(missing))
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Log("node ausente: pulando node --check")
		return
	}
	for _, f := range []string{"app.js", "graph.js", "md.js"} {
		if out, err := exec.Command(node, "--check", filepath.Join("web", f)).CombinedOutput(); err != nil {
			t.Errorf("node --check %s: %v\n%s", f, err, out)
		}
	}
}

func hostCall(t *testing.T, s *server, host string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = host
	s.routes().ServeHTTP(w, req)
	return w
}

func dedup(in []string) []string {
	var out []string
	for i, v := range in {
		if i == 0 || v != in[i-1] {
			out = append(out, v)
		}
	}
	return out
}
