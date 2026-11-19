package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DepConfig descreve uma pasta de dependências (node_modules, vendor...) que o
// git não versiona. Ela nunca é copiada do main; só é instalada se pedido.
type DepConfig struct {
	Dir     string   `json:"dir"`
	Install []string `json:"install"`
}

// DockerEnvConfig aponta o .env do docker compose em que cada worktree precisa
// de nome de projeto e porta próprios para não colidir com as outras.
type DockerEnvConfig struct {
	File       string `json:"file"`
	ProjectKey string `json:"projectKey"`
	PortKey    string `json:"portKey"`
	PortMin    int    `json:"portMin"`
	PortMax    int    `json:"portMax"`
}

type RepoConfig struct {
	Key       string           `json:"key"`
	Dir       string           `json:"dir"`
	Main      string           `json:"main"`
	Base      string           `json:"base"`
	Copy      []string         `json:"copy"`
	Deps      []DepConfig      `json:"deps"`
	DockerEnv *DockerEnvConfig `json:"dockerEnv"`
	// WorktreeRoot é a pasta (relativa ao root) onde as worktrees novas nascem.
	// Padrão: a própria pasta do repo (layout Windows com <repo>/dev como main).
	WorktreeRoot string `json:"worktreeRoot,omitempty"`
	// FolderPattern sobrescreve o padrão global de pasta só para este repo.
	FolderPattern string `json:"folderPattern,omitempty"`
}

type VaultConfig struct {
	Path string `json:"path"`
	// Scope é a pasta do Vault (relativa a Path) que o app lê e escreve; as
	// notas das worktrees ficam em <scope>/worktrees. Padrão: worktree-app.
	Scope string `json:"scope,omitempty"`
	// Savepoints é a pasta dos savepoints (fonte canônica). Os links gravados
	// nas notas apontam para SavepointsLink/<nome>, que é o caminho no Vault.
	Savepoints     string `json:"savepoints,omitempty"`
	SavepointsLink string `json:"savepointsLink,omitempty"`
	// Docs é uma árvore canônica de docs fora do Vault, espelhada nele em
	// DocsLink. O leitor do painel prefere ela por ser mais nova.
	Docs     string `json:"docs,omitempty"`
	DocsLink string `json:"docsLink,omitempty"`
}

// ReaderConfig parametriza o leitor de notas (anexos, imagens, PDF).
type ReaderConfig struct {
	Images         *bool  `json:"images,omitempty"`
	MaxImageWidth  int    `json:"maxImageWidth,omitempty"`
	Attachments    string `json:"attachments,omitempty"` // card | inline
	PDF            string `json:"pdf,omitempty"`         // preview | link
	ExternalImages *bool  `json:"externalImages,omitempty"`
	MaxAssetMB     int    `json:"maxAssetMB,omitempty"`
}

type Config struct {
	Vault             *VaultConfig  `json:"vault,omitempty"`
	Reader            *ReaderConfig `json:"reader,omitempty"`
	Root              string        `json:"root"`
	Listen            string        `json:"listen"`
	TasksDir          string        `json:"tasksDir"`
	WorkspaceTemplate string        `json:"workspaceTemplate"`
	FolderPattern     string        `json:"folderPattern"`
	BranchPattern     string        `json:"branchPattern"`
	DateFormat        string        `json:"dateFormat"`
	Editor            []string      `json:"editor"`
	Repos             []RepoConfig  `json:"repos"`
}

func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(stripJSONC(b)))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Root != "" && !filepath.IsAbs(c.Root) {
		c.Root = filepath.Join(filepath.Dir(path), c.Root)
	}
	if c.Vault != nil && c.Vault.Path != "" && !filepath.IsAbs(c.Vault.Path) {
		c.Vault.Path = filepath.Join(filepath.Dir(path), c.Vault.Path)
	}
	if c.Vault != nil && c.Vault.Savepoints != "" && !filepath.IsAbs(c.Vault.Savepoints) {
		c.Vault.Savepoints = filepath.Join(filepath.Dir(path), c.Vault.Savepoints)
	}
	if c.Vault != nil && c.Vault.Docs != "" && !filepath.IsAbs(c.Vault.Docs) {
		c.Vault.Docs = filepath.Join(filepath.Dir(path), c.Vault.Docs)
	}
	applyDefaults(&c)
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func applyDefaults(c *Config) {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:7777"
	}
	if c.TasksDir == "" {
		c.TasksDir = "tasks"
	}
	if c.FolderPattern == "" {
		c.FolderPattern = "dev.{feature}"
	}
	if c.BranchPattern == "" {
		c.BranchPattern = "dev.{feature}.{date}"
	}
	if c.DateFormat == "" {
		c.DateFormat = "020106"
	}
	if len(c.Editor) == 0 {
		c.Editor = []string{"code"}
	}
	c.Root = filepath.Clean(c.Root)
	if c.Vault != nil {
		setVaultScope(c.Vault.Scope)
		c.Vault.Scope = strings.TrimSuffix(docScope, "/")
		if c.Vault.Savepoints != "" && c.Vault.SavepointsLink == "" {
			c.Vault.SavepointsLink = c.Vault.Scope + "/docs/savepoints"
		}
		if c.Vault.Docs != "" && c.Vault.DocsLink == "" {
			c.Vault.DocsLink = c.Vault.Scope + "/docs"
		}
	}
	if c.Reader == nil {
		c.Reader = &ReaderConfig{}
	}
	if c.Reader.Images == nil {
		t := true
		c.Reader.Images = &t
	}
	if c.Reader.ExternalImages == nil {
		f := false
		c.Reader.ExternalImages = &f
	}
	if c.Reader.MaxImageWidth <= 0 {
		c.Reader.MaxImageWidth = 720
	}
	if c.Reader.Attachments != "inline" {
		c.Reader.Attachments = "card"
	}
	if c.Reader.PDF != "link" {
		c.Reader.PDF = "preview"
	}
	if c.Reader.MaxAssetMB <= 0 {
		c.Reader.MaxAssetMB = assetDefaultMaxMB
	}
	for i := range c.Repos {
		if c.Repos[i].Main == "" {
			c.Repos[i].Main = "dev"
		}
		if c.Repos[i].Base == "" {
			c.Repos[i].Base = "origin/dev"
		}
	}
}

func (c *Config) validate() error {
	if c.Root == "" || c.Root == "." {
		return errors.New("root é obrigatório")
	}
	if !isDir(c.Root) {
		return fmt.Errorf("root %s não existe", c.Root)
	}
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("listen inválido: %w", err)
	}
	// O servidor executa git e mexe em arquivos: só pode aceitar conexões locais.
	if host != "localhost" {
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return errors.New("listen precisa ser 127.0.0.1 ou localhost")
		}
	}
	if !strings.Contains(c.FolderPattern, "{feature}") || !strings.Contains(c.BranchPattern, "{feature}") {
		return errors.New("folderPattern e branchPattern precisam conter {feature}")
	}
	if len(c.Repos) == 0 {
		return errors.New("nenhum repo configurado")
	}
	if c.Vault != nil {
		if s := cleanVaultScope(c.Vault.Scope); strings.Contains(s, "..") || strings.ContainsAny(s, ":\x00[]|") {
			return fmt.Errorf("vault.scope inválido: %q", c.Vault.Scope)
		}
	}
	seen := map[string]bool{}
	for _, r := range c.Repos {
		if r.Key == "" || r.Dir == "" {
			return errors.New("todo repo precisa de key e dir")
		}
		if seen[r.Key] {
			return fmt.Errorf("repo %q repetido", r.Key)
		}
		seen[r.Key] = true
		if d := r.DockerEnv; d != nil && (d.File == "" || d.ProjectKey == "" || d.PortKey == "" || d.PortMin <= 0 || d.PortMax < d.PortMin) {
			return fmt.Errorf("repo %q: dockerEnv incompleto", r.Key)
		}
		if r.FolderPattern != "" && !strings.Contains(r.FolderPattern, "{feature}") {
			return fmt.Errorf("repo %q: folderPattern precisa conter {feature}", r.Key)
		}
		if r.WorktreeRoot != "" && (filepath.IsAbs(r.WorktreeRoot) || !within(c.Root, filepath.Join(c.Root, r.WorktreeRoot))) {
			return fmt.Errorf("repo %q: worktreeRoot precisa ser relativo e ficar dentro do root", r.Key)
		}
		for _, dep := range r.Deps {
			if dep.Dir == "" {
				return fmt.Errorf("repo %q: dep sem dir", r.Key)
			}
		}
	}
	return nil
}

func (c *Config) readerImages() bool {
	return c.Reader == nil || c.Reader.Images == nil || *c.Reader.Images
}

func (c *Config) readerExternalImages() bool {
	return c.Reader != nil && c.Reader.ExternalImages != nil && *c.Reader.ExternalImages
}

func (c *Config) readerState() ReaderState {
	return ReaderState{
		Images:         c.readerImages(),
		MaxImageWidth:  c.Reader.MaxImageWidth,
		Attachments:    c.Reader.Attachments,
		PDF:            c.Reader.PDF,
		ExternalImages: c.readerExternalImages(),
	}
}

func (c *Config) repo(key string) *RepoConfig {
	for i := range c.Repos {
		if c.Repos[i].Key == key {
			return &c.Repos[i]
		}
	}
	return nil
}

// containerPath é onde as worktrees novas do repo nascem (e onde o alocador de
// portas procura .env de outras worktrees).
func (c *Config) containerPath(r *RepoConfig) string {
	if r.WorktreeRoot != "" {
		return filepath.Join(c.Root, r.WorktreeRoot)
	}
	return filepath.Join(c.Root, r.Dir)
}

func (c *Config) mainPath(r *RepoConfig) string   { return filepath.Join(c.Root, r.Dir, r.Main) }
func (c *Config) tasksPath(feature string) string { return filepath.Join(c.Root, c.TasksDir, feature) }

func (c *Config) workspacePath(feature string) string {
	return filepath.Join(c.Root, feature+".code-workspace")
}

func (c *Config) folderPattern(r *RepoConfig) string {
	if r != nil && r.FolderPattern != "" {
		return r.FolderPattern
	}
	return c.FolderPattern
}

func (c *Config) folderName(r *RepoConfig, feature string) string {
	return strings.ReplaceAll(c.folderPattern(r), "{feature}", feature)
}

func (c *Config) branchName(feature string, t time.Time) string {
	s := strings.ReplaceAll(c.BranchPattern, "{feature}", feature)
	return strings.ReplaceAll(s, "{date}", t.Format(c.DateFormat))
}

// featureFromFolder desfaz o folderPattern; pastas fora do padrão viram a
// própria feature (ex.: "fixes-211026").
func (c *Config) featureFromFolder(r *RepoConfig, folder string) string {
	prefix, suffix, _ := strings.Cut(c.folderPattern(r), "{feature}")
	if len(folder) > len(prefix)+len(suffix) && strings.HasPrefix(folder, prefix) && strings.HasSuffix(folder, suffix) {
		return folder[len(prefix) : len(folder)-len(suffix)]
	}
	return folder
}
