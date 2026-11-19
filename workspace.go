package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type wsFolder struct {
	Name string `json:"name,omitempty"`
	Path string `json:"path"`
}

func readWorkspaceFolders(path string) ([]wsFolder, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ws struct {
		Folders []wsFolder `json:"folders"`
	}
	if err := json.Unmarshal(stripJSONC(b), &ws); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return ws.Folders, nil
}

// workspaceFolderPaths devolve os caminhos absolutos das pastas do workspace.
func workspaceFolderPaths(root, path string) ([]string, error) {
	folders, err := readWorkspaceFolders(path)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(folders))
	for _, f := range folders {
		p := filepath.FromSlash(f.Path)
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		out = append(out, filepath.Clean(p))
	}
	return out, nil
}

// buildWorkspace monta o .code-workspace com as pastas dadas e o resto
// (settings, extensions...) copiado do template.
func buildWorkspace(templatePath string, folders []wsFolder) ([]byte, error) {
	rest := map[string]json.RawMessage{}
	if templatePath != "" {
		if b, err := os.ReadFile(templatePath); err == nil {
			if err := json.Unmarshal(stripJSONC(b), &rest); err != nil {
				return nil, fmt.Errorf("template %s: %w", filepath.Base(templatePath), err)
			}
		}
	}
	delete(rest, "folders")
	if err := moveSettingsIntoPlace(rest); err != nil {
		return nil, fmt.Errorf("template %s: %w", filepath.Base(templatePath), err)
	}
	keys := make([]string, 0, len(rest))
	for k := range rest {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if (keys[i] == "settings") != (keys[j] == "settings") {
			return keys[i] == "settings"
		}
		return keys[i] < keys[j]
	})

	var buf bytes.Buffer
	fj, err := json.MarshalIndent(folders, "  ", "  ")
	if err != nil {
		return nil, err
	}
	buf.WriteString("{\n  \"folders\": ")
	buf.Write(fj)
	for _, k := range keys {
		var ind bytes.Buffer
		if err := json.Indent(&ind, rest[k], "  ", "  "); err != nil {
			return nil, err
		}
		kj, _ := json.Marshal(k)
		buf.WriteString(",\n  ")
		buf.Write(kj)
		buf.WriteString(": ")
		buf.Write(ind.Bytes())
	}
	buf.WriteString("\n}\n")
	return buf.Bytes(), nil
}

// workspaceRootKeys são as chaves que o VS Code lê na raiz de um
// .code-workspace; qualquer outra só tem efeito dentro de "settings".
var workspaceRootKeys = map[string]bool{
	"folders": true, "settings": true, "extensions": true, "launch": true, "tasks": true, "remoteAuthority": true,
}

// moveSettingsIntoPlace leva para "settings" as configurações que o template
// deixou na raiz. Se a chave já existe em settings e as duas são objetos, os
// dois são juntados; senão vale o que já estava em settings.
func moveSettingsIntoPlace(rest map[string]json.RawMessage) error {
	settings := map[string]json.RawMessage{}
	if raw, ok := rest["settings"]; ok {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return fmt.Errorf("settings: %w", err)
		}
	}
	moved := false
	for k, v := range rest {
		if workspaceRootKeys[k] {
			continue
		}
		delete(rest, k)
		moved = true
		if cur, dup := settings[k]; dup {
			settings[k] = mergeObjects(cur, v)
		} else {
			settings[k] = v
		}
	}
	if !moved {
		return nil
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	rest["settings"] = b
	return nil
}

// mergeObjects junta dois objetos JSON (as chaves de base vencem); se algum
// dos dois não for objeto, fica base.
func mergeObjects(base, extra json.RawMessage) json.RawMessage {
	var a, b map[string]json.RawMessage
	if json.Unmarshal(base, &a) != nil || json.Unmarshal(extra, &b) != nil || a == nil || b == nil {
		return base
	}
	for k, v := range b {
		if _, ok := a[k]; !ok {
			a[k] = v
		}
	}
	out, err := json.Marshal(a)
	if err != nil {
		return base
	}
	return out
}

// workspaceIndex mapeia cada pasta citada em algum .code-workspace do root
// para os workspaces que a citam.
func workspaceIndex(cfg *Config) map[string][]string {
	idx := map[string][]string{}
	matches, _ := filepath.Glob(filepath.Join(cfg.Root, "*.code-workspace"))
	for _, m := range matches {
		paths, err := workspaceFolderPaths(cfg.Root, m)
		if err != nil {
			continue
		}
		for _, p := range paths {
			idx[pathKey(p)] = append(idx[pathKey(p)], filepath.Base(m))
		}
	}
	return idx
}

func ensureTasks(cfg *Config, feature string, items []PlanItem, lg Logger) {
	dir := cfg.tasksPath(feature)
	rel := relSlash(cfg.Root, dir)
	if isDir(dir) {
		lg.Log("", "info", rel+" já existe, mantida")
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n| Repo | Pasta | Branch |\n|---|---|---|\n", feature)
	for _, it := range items {
		r := cfg.repo(it.Repo)
		fmt.Fprintf(&b, "| %s | %s | `%s` |\n", r.Dir, relSlash(cfg.Root, it.Path), it.Branch)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		lg.Log("", "warn", "não deu para criar "+rel+": "+err.Error())
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(b.String()), 0o644); err != nil {
		lg.Log("", "warn", "não deu para escrever o README de "+rel+": "+err.Error())
		return
	}
	lg.Log("", "ok", "criada "+rel+"/README.md")
}

func ensureWorkspace(cfg *Config, feature string, items []PlanItem, lg Logger) {
	p := cfg.workspacePath(feature)
	name := filepath.Base(p)
	if exists(p) {
		lg.Log("", "warn", name+" já existe e não foi alterado; inclua as pastas novas nele se quiser")
		return
	}
	var folders []wsFolder
	for _, it := range items {
		folders = append(folders, wsFolder{Name: cfg.repo(it.Repo).Dir, Path: relSlash(cfg.Root, it.Path)})
	}
	if td := cfg.tasksPath(feature); isDir(td) {
		folders = append(folders, wsFolder{Name: "tasks", Path: strings.TrimPrefix(relSlash(cfg.Root, td), "./")})
	}
	tpl := ""
	if cfg.WorkspaceTemplate != "" {
		tpl = filepath.Join(cfg.Root, cfg.WorkspaceTemplate)
	}
	b, err := buildWorkspace(tpl, folders)
	if err != nil {
		lg.Log("", "warn", "workspace não gerado: "+err.Error())
		return
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		lg.Log("", "warn", "workspace não gravado: "+err.Error())
		return
	}
	lg.Log("", "ok", "gerado "+name)
}
