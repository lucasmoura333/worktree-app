package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
