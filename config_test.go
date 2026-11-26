package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeConfig grava um config.json numa pasta temporária com uma pasta
// "projects" (o root) ao lado, e devolve o caminho do arquivo.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := realPath(t.TempDir())
	if err := os.MkdirAll(filepath.Join(dir, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "config.json")
	write(t, p, body)
	return p
}

func TestLoadConfigJSONCAndDefaults(t *testing.T) {
	t.Setenv("WT_STATE_DIR", "")
	t.Setenv("WSL_DISTRO_NAME", "Exemplo")
	t.Cleanup(func() { setVaultScope("") })
	p := writeConfig(t, `{
  // comentário de linha
  "root": "projects",
  /* bloco */
  "repos": [
    { "key": "app", "dir": "acme-app", }, // vírgula sobrando
  ],
  "stateDir": "estado",
  "vault": { "path": "vault", "savepoints": "sp", "docs": "docs", "scope": "Time/Proj" },
}`)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(p)

	// Caminhos relativos ficam relativos à pasta do config.
	for name, got := range map[string]string{
		"root":             cfg.Root,
		"stateDir":         cfg.StateDir,
		"vault.path":       cfg.Vault.Path,
		"vault.savepoints": cfg.Vault.Savepoints,
		"vault.docs":       cfg.Vault.Docs,
	} {
		if !filepath.IsAbs(got) || !strings.HasPrefix(got, dir) {
			t.Errorf("%s = %q, queria absoluto dentro de %s", name, got, dir)
		}
	}

	// Padrões.
	r := cfg.Repos[0]
	checks := map[string][2]string{
		"listen":        {cfg.Listen, "127.0.0.1:7777"},
		"tasksDir":      {cfg.TasksDir, "tasks"},
		"folderPattern": {cfg.FolderPattern, "dev.{feature}"},
		"branchPattern": {cfg.BranchPattern, "dev.{feature}.{date}"},
		"dateFormat":    {cfg.DateFormat, "020106"},
		"repo.main":     {r.Main, "dev"},
		"repo.base":     {r.Base, "origin/dev"},
		"vault.scope":   {cfg.Vault.Scope, "Time/Proj"},
		"savepointsLnk": {cfg.Vault.SavepointsLink, "Time/Proj/docs/savepoints"},
		"docsLink":      {cfg.Vault.DocsLink, "Time/Proj/docs"},
		"reader.att":    {cfg.Reader.Attachments, "card"},
		"reader.pdf":    {cfg.Reader.PDF, "preview"},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %q, queria %q", name, c[0], c[1])
		}
	}
	if len(cfg.Editor) != 1 || cfg.Editor[0] != "code" {
		t.Errorf("editor = %v", cfg.Editor)
	}
	if cfg.Snapshots == nil || cfg.Snapshots.Keep <= 0 || cfg.Snapshots.MaxMB <= 0 {
		t.Errorf("snapshots sem padrão: %+v", cfg.Snapshots)
	}
	if cfg.DiffMaxKB != diffDefaultMaxKB || len(cfg.Agents) == 0 {
		t.Errorf("diff/agents sem padrão: %d %v", cfg.DiffMaxKB, cfg.Agents)
	}
	// Terminal padrão só existe no WSL (Linux com distro); fora dele fica vazio.
	if runtime.GOOS == "linux" && (cfg.Distro != "Exemplo" || len(cfg.Terminal) == 0) {
		t.Errorf("WSL: distro %q, terminal %v", cfg.Distro, cfg.Terminal)
	}
	t.Setenv("WSL_DISTRO_NAME", "")
	if c2, err := loadConfig(p); err != nil || len(c2.Terminal) != 0 {
		t.Errorf("sem distro o terminal deveria ficar vazio: %v %v", c2.Terminal, err)
	}
	if cfg.Reader.Images == nil || !*cfg.Reader.Images || cfg.Reader.ExternalImages == nil || *cfg.Reader.ExternalImages {
		t.Errorf("reader images: %+v", cfg.Reader)
	}
}

func TestLoadConfigKeepsExplicitValues(t *testing.T) {
	root := realPath(t.TempDir())
	p := writeConfig(t, `{
  "root": `+jsonQuote(root)+`,
  "listen": "localhost:7801",
  "folderPattern": "{feature}",
  "branchPattern": "feat/{feature}",
  "editor": ["cursor", "-n"],
  "snapshots": { "keep": 3 },
  "repos": [{ "key": "api", "dir": "api", "main": "main", "base": "origin/main" }]
}`)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Root != root || cfg.Listen != "localhost:7801" || cfg.FolderPattern != "{feature}" || cfg.BranchPattern != "feat/{feature}" {
		t.Fatalf("valores explícitos perdidos: %+v", cfg)
	}
	if cfg.Repos[0].Main != "main" || cfg.Repos[0].Base != "origin/main" || len(cfg.Editor) != 2 {
		t.Fatalf("repo/editor: %+v %v", cfg.Repos[0], cfg.Editor)
	}
	d := defaultSnapshotConfig()
	if cfg.Snapshots.Keep != 3 || cfg.Snapshots.MaxAgeDays != d.MaxAgeDays {
		t.Fatalf("snapshots: %+v", cfg.Snapshots)
	}
}

// Negativo em snapshots é erro de config: não pode cair no padrão em silêncio.
// O git devolve caminhos resolvidos; um root por symlink precisa bater com eles.
func TestLoadConfigResolvesRootLink(t *testing.T) {
	p := writeConfig(t, `{"root":"atalho","repos":[{"key":"a","dir":"a"}]}`)
	dir := filepath.Dir(p)
	if err := os.Symlink(filepath.Join(dir, "projects"), filepath.Join(dir, "atalho")); err != nil {
		t.Skip("sem symlink:", err)
	}
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "projects"); cfg.Root != want {
		t.Fatalf("root = %q, quer %q", cfg.Root, want)
	}
}

func TestLoadConfigRejectsNegativeSnapshot(t *testing.T) {
	for _, field := range []string{"keep", "maxAgeDays", "preRemoveDays", "maxMB", "fileMaxMB"} {
		p := writeConfig(t, `{"root":"projects","snapshots":{"`+field+`":-1},"repos":[{"key":"a","dir":"a"}]}`)
		if _, err := loadConfig(p); err == nil || !strings.Contains(err.Error(), "negativos") {
			t.Errorf("%s=-1 deveria ser recusado: %v", field, err)
		}
	}
	// Zero (ou ausente) continua caindo no padrão.
	p := writeConfig(t, `{"root":"projects","snapshots":{"keep":0},"repos":[{"key":"a","dir":"a"}]}`)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if d := defaultSnapshotConfig(); cfg.Snapshots.Keep != d.Keep {
		t.Fatalf("keep 0 = %d, queria o padrão %d", cfg.Snapshots.Keep, d.Keep)
	}
}

func jsonQuote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

func TestLoadConfigValidation(t *testing.T) {
	repo := `"repos": [{ "key": "app", "dir": "app" }]`
	cases := map[string]struct{ body, want string }{
		"campo desconhecido":   {`{"root":"projects",` + repo + `,"naoExiste":1}`, "unknown field"},
		"json quebrado":        {`{"root":`, "config.json"},
		"sem root":             {`{` + repo + `}`, "root é obrigatório"},
		"root inexistente":     {`{"root":"sumiu",` + repo + `}`, "não existe"},
		"listen público":       {`{"root":"projects","listen":"0.0.0.0:7777",` + repo + `}`, "127.0.0.1 ou localhost"},
		"listen sem porta":     {`{"root":"projects","listen":"127.0.0.1",` + repo + `}`, "listen inválido"},
		"pattern sem feature":  {`{"root":"projects","folderPattern":"x",` + repo + `}`, "{feature}"},
		"sem repos":            {`{"root":"projects","repos":[]}`, "nenhum repo"},
		"repo sem dir":         {`{"root":"projects","repos":[{"key":"app"}]}`, "key e dir"},
		"repo repetido":        {`{"root":"projects","repos":[{"key":"a","dir":"a"},{"key":"a","dir":"b"}]}`, "repetido"},
		"dockerEnv incompleto": {`{"root":"projects","repos":[{"key":"a","dir":"a","dockerEnv":{"file":".env"}}]}`, "dockerEnv"},
		"worktreeRoot fora":    {`{"root":"projects","repos":[{"key":"a","dir":"a","worktreeRoot":"../fora"}]}`, "worktreeRoot"},
		"dep sem dir":          {`{"root":"projects","repos":[{"key":"a","dir":"a","deps":[{"install":["x"]}]}]}`, "dep sem dir"},
		"vault scope com ..":   {`{"root":"projects","vault":{"path":"v","scope":"../x"},` + repo + `}`, "vault.scope"},
	}
	t.Cleanup(func() { setVaultScope("") })
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadConfig(writeConfig(t, tc.body))
			if err == nil {
				t.Fatalf("deveria falhar")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("erro %q não contém %q", err, tc.want)
			}
		})
	}
	if _, err := loadConfig(filepath.Join(t.TempDir(), "nao-existe.json")); err == nil {
		t.Fatal("arquivo ausente deveria falhar")
	}
}

// O config de exemplo do repositório precisa continuar carregando.
func TestExampleConfigLoads(t *testing.T) {
	t.Cleanup(func() { setVaultScope("") })
	for _, p := range []string{"config.example.json", filepath.Join("examples", "config.example.json")} {
		if !exists(p) {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var probe Config
		dec := json.NewDecoder(bytes.NewReader(stripJSONC(b)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&probe); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}
