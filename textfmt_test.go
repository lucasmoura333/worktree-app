package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStripJSONC(t *testing.T) {
	src := `{
  // comentário
  "folders": [
    { "path": "./a//b", }, /* bloco */
    // { "path": "./comentada" },
  ],
  "settings": { "x": "tem, vírgula e \"aspas\" // não é comentário", },
}`
	var v struct {
		Folders  []map[string]string `json:"folders"`
		Settings map[string]string   `json:"settings"`
	}
	if err := json.Unmarshal(stripJSONC([]byte(src)), &v); err != nil {
		t.Fatalf("json inválido: %v\n%s", err, stripJSONC([]byte(src)))
	}
	if len(v.Folders) != 1 || v.Folders[0]["path"] != "./a//b" {
		t.Fatalf("folders = %v", v.Folders)
	}
	if v.Settings["x"] != `tem, vírgula e "aspas" // não é comentário` {
		t.Fatalf("string alterada: %q", v.Settings["x"])
	}
}

func TestEnvValues(t *testing.T) {
	src := "# topo\r\nCOMPOSE_PROJECT_NAME=\"acme-marco\"\r\nexport DEV_PORT=6660 # porta\r\nOUTRA=1\r\n"
	if v, _ := getEnvValue(src, "COMPOSE_PROJECT_NAME"); v != "acme-marco" {
		t.Fatalf("project = %q", v)
	}
	if v, _ := getEnvValue(src, "DEV_PORT"); v != "6660" {
		t.Fatalf("port = %q", v)
	}
	out := setEnvValue(src, "DEV_PORT", "6661")
	out = setEnvValue(out, "NOVA", "x")
	want := "# topo\r\nCOMPOSE_PROJECT_NAME=\"acme-marco\"\r\nDEV_PORT=6661\r\nOUTRA=1\r\nNOVA=x\r\n"
	if out != want {
		t.Fatalf("setEnvValue:\n%q\nquer\n%q", out, want)
	}
}

func TestComposeName(t *testing.T) {
	if got := composeName("acme-Marco-dev.3d_poc"); got != "acme-marco-dev-3d_poc" {
		t.Fatalf("composeName = %q", got)
	}
}

func TestFeatureFromFolder(t *testing.T) {
	c := &Config{FolderPattern: "dev.{feature}"}
	cases := map[string]string{"dev.bdgd": "bdgd", "fixes-211026": "fixes-211026", "dev.": "dev.", "dev.a.review": "a.review"}
	for in, want := range cases {
		if got := c.featureFromFolder(nil, in); got != want {
			t.Errorf("%s -> %s, quer %s", in, got, want)
		}
	}
}

func TestValidRef(t *testing.T) {
	for _, s := range []string{"origin/dev", "dev.bdgd.291026", "feat/x-y"} {
		if !validRef(s) {
			t.Errorf("%s deveria ser válido", s)
		}
	}
	for _, s := range []string{"", "-f", "--upload-pack=x", "a b", "a~1"} {
		if validRef(s) {
			t.Errorf("%q deveria ser inválido", s)
		}
	}
}

// gitRefOK segue as regras do "git check-ref-format" para refs/heads/...
func gitRefOK(ref string) bool {
	if ref == "" || ref == "@" || !strings.Contains(ref, "/") ||
		strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") ||
		strings.Contains(ref, "//") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") ||
		strings.ContainsAny(ref, " ~^:?*[\\") {
		return false
	}
	for i := 0; i < len(ref); i++ {
		if ref[i] < 0x20 || ref[i] == 0x7f {
			return false
		}
	}
	for _, seg := range strings.Split(ref, "/") {
		if strings.HasPrefix(seg, ".") || strings.HasSuffix(seg, ".lock") {
			return false
		}
	}
	return true
}

func TestGitRefOKMatchesGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git ausente")
	}
	for _, ref := range []string{
		"refs/heads/dev.x", "refs/heads/dev.x.lock", "refs/heads/a..b", "refs/heads/a.",
		"refs/heads/.a", "refs/heads/a@{1}", "refs/heads/a b", "refs/heads/a~1",
		"refs/heads/a//b", "refs/heads/a/", "refs/heads/feat/x-y", "refs/heads/a\x7fb",
		"refs/heads/ação", "refs/heads/a\\b", "refs/heads/a.lock/b",
	} {
		want := exec.Command("git", "check-ref-format", ref).Run() == nil
		if got := gitRefOK(ref); got != want {
			t.Errorf("%q: gitRefOK=%v, git=%v", ref, got, want)
		}
	}
}

func TestBranchRefOK(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git ausente")
	}
	dir := t.TempDir()
	for name, want := range map[string]bool{
		"dev.bdgd": true, "dev.bdgd.261126": true, "feat/x": true,
		"dev.lock": false, "dev.x.lock": false, "dev..x": false, "-x": false, "": false, "a b": false,
	} {
		if got := branchRefOK(context.Background(), dir, name); got != want {
			t.Errorf("branchRefOK(%q) = %v, quer %v", name, got, want)
		}
	}
}

func FuzzValidFeature(f *testing.F) {
	for _, s := range []string{"bdgd", "fix-211026", "a.b_c-d", "x.lock", "a..b", "a.", "-x", ".x",
		"a/b", "CON", strings.Repeat("a", 64), strings.Repeat("a", 65), "ação", "a b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if validFeature(name) != nil {
			return
		}
		if len(name) > 64 || name == "." || name == ".." || filepath.Base(name) != name ||
			strings.ContainsAny(name, `/\`) {
			t.Fatalf("%q aceito, mas não serve de nome de pasta", name)
		}
		// Com o nome no fim ("dev.lock") quem barra é o buildPlan.
		for _, br := range []string{name, "dev." + name + ".261126", "feat/" + name} {
			if !gitRefOK("refs/heads/" + br) {
				t.Fatalf("%q aceito, mas a branch %q é inválida", name, br)
			}
		}
	})
}

func FuzzValidRef(f *testing.F) {
	for _, s := range []string{"origin/dev", "dev.bdgd.291026", "feat/x-y", "-f", "--upload-pack=x",
		"a b", "a~1", "a\x00b", "a\x1bb", "a\x7fb", "HEAD@{1}", "abc123"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !validRef(s) {
			return
		}
		if strings.HasPrefix(s, "-") {
			t.Fatalf("%q aceito, mas o git leria como opção", s)
		}
		for i := 0; i < len(s); i++ {
			if s[i] <= ' ' || s[i] == 0x7f {
				t.Fatalf("%q aceito com byte 0x%02x", s, s[i])
			}
		}
	})
}

func TestShippedConfigParses(t *testing.T) {
	b, err := os.ReadFile("config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(stripJSONC(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("config.example.json: %v", err)
	}
	c.Root = t.TempDir()
	applyDefaults(&c)
	if err := c.validate(); err != nil {
		t.Fatalf("config.example.json: %v", err)
	}
}

// Templates com configurações soltas na raiz (como o dev.code-workspace) geram
// workspaces com tudo dentro de "settings" e as chaves repetidas juntadas.
func TestBuildWorkspaceMovesRootSettings(t *testing.T) {
	tpl := filepath.Join(t.TempDir(), "dev.code-workspace")
	if err := os.WriteFile(tpl, []byte(`{
  "folders": [{ "path": "./x" }],
  "settings": { "files.watcherExclude": { "**/node_modules/**": true }, "a": 1, },
  "a": 2,
  "[php]": { "editor.defaultFormatter": "bmewburn.vscode-intelephense-client" },
  "files.watcherExclude": { "**/vendor/**": true, "**/node_modules/**": false },
  "extensions": { "recommendations": [] },
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := buildWorkspace(tpl, []wsFolder{{Name: "app", Path: "./acme-app/dev.x"}})
	if err != nil {
		t.Fatal(err)
	}
	var ws map[string]json.RawMessage
	if err := json.Unmarshal(b, &ws); err != nil {
		t.Fatalf("json inválido: %v\n%s", err, b)
	}
	for k := range ws {
		if !workspaceRootKeys[k] {
			t.Errorf("%q ficou na raiz", k)
		}
	}
	var s struct {
		A       int             `json:"a"`
		PHP     json.RawMessage `json:"[php]"`
		Watcher map[string]bool `json:"files.watcherExclude"`
	}
	if err := json.Unmarshal(ws["settings"], &s); err != nil {
		t.Fatal(err)
	}
	if s.A != 1 || s.PHP == nil {
		t.Errorf("settings = %s", ws["settings"])
	}
	if !s.Watcher["**/node_modules/**"] || !s.Watcher["**/vendor/**"] || len(s.Watcher) != 2 {
		t.Errorf("watcherExclude não juntou: %v", s.Watcher)
	}
	if ws["extensions"] == nil {
		t.Error("extensions sumiu da raiz")
	}
}
