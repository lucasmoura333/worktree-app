package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadFrontmatterSubset(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sp.md")
	write(t, p, `---
id: savepoint-x
date: 2026-11-10
branch: "dev.checkout"
branches: [feat/a, 'feat/b']
taxonomy:
  domain: produto
related:
  - "[[worktree-app/docs/kanbans/notificacoes|Kanban]]"
  - "[[worktree-app/Diario/2026/11 - Novembro/11-10-2026]]"
worktree: "[[worktree-app/worktrees/checkout--app]]"
---
branch: dev.no-corpo
`)
	fm := readFrontmatter(p)
	if fm == nil {
		t.Fatal("frontmatter não lido")
	}
	if got := strings.Join(append(fm["branch"], fm["branches"]...), ","); got != "dev.checkout,feat/a,feat/b" {
		t.Fatalf("branches = %s", got)
	}
	if len(fm["related"]) != 2 || fm["domain"] != nil {
		t.Fatalf("related/taxonomy = %v / %v", fm["related"], fm["domain"])
	}
	if got := frontFrom(fm); got != "[[worktree-app/docs/kanbans/notificacoes]]" {
		t.Fatalf("front = %s", got)
	}
}

func spFixture(t *testing.T) (*Config, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &Config{Vault: &VaultConfig{Path: t.TempDir(), Savepoints: dir}}
	applyDefaults(cfg)
	return cfg, dir
}

func TestSavepointLinksNewestRepoFilterAndNote(t *testing.T) {
	cfg, dir := spFixture(t)
	write(t, filepath.Join(dir, "2026-11-01-velho.md"), "---\ndate: 2026-11-01\nbranch: dev.x\nrelated: [\"[[worktree-app/docs/kanbans/frente-a]]\"]\n---\n")
	write(t, filepath.Join(dir, "2026-11-05-novo.md"), "---\ndate: 2026-11-05\nbranch: dev.x\n---\n")
	write(t, filepath.Join(dir, "2026-11-09-outro-repo.md"), "---\ndate: 2026-11-09\nbranch: dev.x\nrepo: acme-api\n---\n")
	write(t, filepath.Join(dir, "2026-11-03-por-nota.md"), "---\ndate: 2026-11-03\nworktree: \"[[worktree-app/worktrees/y--app]]\"\n---\n")
	write(t, filepath.Join(dir, "2026-11-20-so-texto.md"), "---\ndate: 2026-11-20\n---\nbranch: dev.x no corpo não conta\n")
	write(t, filepath.Join(dir, "_INDEX.md"), "---\nbranch: dev.x\ndate: 2099-01-01\n---\n")

	ix := loadSavepointLinks(cfg)
	l := ix.find(WorktreeInfo{Repo: "app", RepoDir: "acme-app", Branch: "dev.x"}, "x--app")
	if l == nil || l.Savepoint != "[[worktree-app/docs/savepoints/2026-11-05-novo]]" {
		t.Fatalf("esperava o mais novo do repo, veio %+v", l)
	}
	if l.Front != "" {
		t.Fatalf("o savepoint mais novo não tem frente; veio %q", l.Front)
	}
	if l := ix.find(WorktreeInfo{Repo: "api", RepoDir: "acme-api", Branch: "dev.x"}, "x--api"); l == nil || !strings.Contains(l.Savepoint, "outro-repo") {
		t.Fatalf("filtro de repo: %+v", l)
	}
	if l := ix.find(WorktreeInfo{Repo: "app", RepoDir: "acme-app"}, "y--app"); l == nil || !strings.Contains(l.Savepoint, "por-nota") {
		t.Fatalf("vínculo pela nota (detached): %+v", l)
	}
}

func TestApplySavepointLinkRespectsManual(t *testing.T) {
	l := &savepointLink{Savepoint: "[[sp/novo]]", Front: "[[k/auto]]"}
	auto := &Connection{LastSavepoint: "[[sp/velho]]", Source: "auto"}
	applySavepointLink(auto, l)
	if auto.LastSavepoint != "[[sp/novo]]" || auto.Front != "[[k/auto]]" {
		t.Fatalf("auto não atualizou: %+v", auto)
	}
	manual := &Connection{Front: "[[k/manual]]", Source: "manual"}
	applySavepointLink(manual, l)
	if manual.Front != "[[k/manual]]" || manual.LastSavepoint != "[[sp/novo]]" || manual.Source != "manual" {
		t.Fatalf("manual: %+v", manual)
	}
	empty := &Connection{}
	applySavepointLink(empty, l)
	if empty.Source != "auto" {
		t.Fatalf("source = %q", empty.Source)
	}
}

// A nota só é regravada quando algo além do updated_at muda.
func TestVaultNoteNotRewrittenWhenUnchanged(t *testing.T) {
	s, st, dir := vaultFixture(t)
	s.syncVault(st)
	if st.VaultError != "" {
		t.Fatal(st.VaultError)
	}
	path := filepath.Join(dir, "checkout--app.md")
	canvas := filepath.Join(dir, "worktrees.canvas")
	old := time.Now().Add(-time.Hour)
	for _, p := range []string{path, canvas} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	before := read(t, path)
	s.syncVault(st)
	for _, p := range []string{path, canvas} {
		if info, _ := os.Stat(p); info.ModTime().After(old.Add(time.Minute)) {
			t.Fatalf("%s regravado sem mudança", filepath.Base(p))
		}
	}
	st.Features[0].Worktrees[0].Dirty = 7
	s.syncVault(st)
	if after := read(t, path); after == before || !strings.Contains(after, "dirty: 7") {
		t.Fatal("mudança real não gravada")
	}
}

func TestVaultDerivesLinksFromSavepoints(t *testing.T) {
	s, st, dir := vaultFixture(t)
	sp := t.TempDir()
	s.cfg.Vault.Savepoints, s.cfg.Vault.SavepointsLink = sp, "worktree-app/docs/savepoints"
	write(t, filepath.Join(sp, "2026-11-10-chk.md"), "---\ndate: 2026-11-10\nbranch: dev.checkout\nrelated:\n  - \"[[worktree-app/docs/kanbans/notificacoes]]\"\n---\n")
	s.syncVault(st)
	n, err := readNote(filepath.Join(dir, "checkout--app.md"))
	if err != nil {
		t.Fatal(err)
	}
	c := n.WT.Connection
	if c.LastSavepoint != "[[worktree-app/docs/savepoints/2026-11-10-chk]]" || c.Front != "[[worktree-app/docs/kanbans/notificacoes]]" || c.Source != "auto" {
		t.Fatalf("vínculo derivado: %+v", c)
	}
}

// Pasta renomeada (mesmo repo e branch) herda os vínculos; a nota antiga sai
// em vez de virar "removido". Worktree aninhada no main não vira nota.
func TestVaultRenameAndNestedWorktrees(t *testing.T) {
	s, st, dir := vaultFixture(t)
	s.cfg.Root = t.TempDir()
	wt := &st.Features[0].Worktrees[0]
	wt.Path = filepath.Join(s.cfg.Root, "acme-app-checkout")
	wt.Feature, st.Features[0].Name = "acme-app-checkout", "acme-app-checkout"
	s.syncVault(st)
	oldPath := filepath.Join(dir, "acme-app-checkout--app.md")
	n, err := readNote(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	n.WT.Connection.Front, n.WT.Connection.Source = "[[worktree-app/docs/kanbans/notificacoes]]", "manual"
	if err := writeNote(dir, n); err != nil {
		t.Fatal(err)
	}

	wt.Feature, st.Features[0].Name = "checkout", "checkout"
	bench := WorktreeInfo{Feature: "pr", Repo: "app", RepoDir: "acme-app", Detached: true, Head: "2b3c4d5e6",
		Path: filepath.Join(s.cfg.Root, "acme-app", ".bench", "pr")}
	st.Features = append(st.Features, FeatureState{Name: "pr", Worktrees: []WorktreeInfo{bench}})
	s.syncVault(st)
	if st.VaultError != "" {
		t.Fatal(st.VaultError)
	}
	if exists(oldPath) {
		t.Fatal("nota antiga deveria sair após o rename")
	}
	n, err = readNote(filepath.Join(dir, "checkout--app.md"))
	if err != nil || n.WT.Connection.Front != "[[worktree-app/docs/kanbans/notificacoes]]" || n.WT.Connection.Source != "manual" {
		t.Fatalf("rename perdeu vínculo: %v %+v", err, n.WT.Connection)
	}
	if exists(filepath.Join(dir, "pr--app.md")) || len(st.Archived) != 0 {
		t.Fatal("worktree aninhada não pode virar nota nem arquivo de removido")
	}

	// Detached (sem branch): o rename casa pelo SHA.
	det := WorktreeInfo{Feature: "acme-app-manual", Repo: "app", RepoDir: "acme-app", Detached: true, Head: "3c4d5e6f7",
		Path: filepath.Join(s.cfg.Root, "acme-app-manual")}
	st.Features = append(st.Features, FeatureState{Name: det.Feature, Worktrees: []WorktreeInfo{det}})
	s.syncVault(st)
	st.Features[len(st.Features)-1].Worktrees[0].Feature = "manual"
	st.Features[len(st.Features)-1].Name = "manual"
	s.syncVault(st)
	if exists(filepath.Join(dir, "acme-app-manual--app.md")) || !exists(filepath.Join(dir, "manual--app.md")) || len(st.Archived) != 0 {
		t.Fatal("worktree aninhada não pode virar nota nem arquivo de removido")
	}
}

// peekVault (estado durante operação) não grava nada.
func TestPeekVaultIsReadOnly(t *testing.T) {
	s, st, dir := vaultFixture(t)
	s.peekVault(st)
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("peek gravou %d arquivos", len(entries))
	}
}

func TestPlanRefusesWorktreeInsideMain(t *testing.T) {
	cfg := fixture(t)
	r := &cfg.Repos[0]
	// Layout WSL: o repo é o próprio main ("."); sem worktreeRoot a feature
	// nasceria dentro dele.
	if err := os.Rename(filepath.Join(cfg.Root, "acme-app", "dev"), filepath.Join(cfg.Root, "tmp-main")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(cfg.Root, "acme-app")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(cfg.Root, "tmp-main"), filepath.Join(cfg.Root, "acme-app")); err != nil {
		t.Fatal(err)
	}
	r.Main = "."
	p := buildPlan(t.Context(), cfg, &CreateReq{Feature: "x", Repos: []CreateRepoReq{{Key: "app"}}}, newPortAllocator(), false)
	if p.OK || !strings.Contains(strings.Join(p.Items[0].Problems, "|"), "dentro do repo principal") {
		t.Fatalf("plano deveria recusar: %+v", p.Items)
	}
	r.WorktreeRoot, r.FolderPattern = ".", "acme-app-{feature}"
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	p = buildPlan(t.Context(), cfg, &CreateReq{Feature: "x", Repos: []CreateRepoReq{{Key: "app"}}}, newPortAllocator(), false)
	if !p.OK || p.Items[0].Path != filepath.Join(cfg.Root, "acme-app-x") {
		t.Fatalf("plano com worktreeRoot: %+v", p.Items)
	}
	if got := cfg.featureFromFolder(r, "acme-app-x"); got != "x" {
		t.Fatalf("featureFromFolder = %s", got)
	}
}
