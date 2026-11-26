package main

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func createForSnapshot(t *testing.T, cfg *Config, feature string) WorktreeInfo {
	t.Helper()
	req := &CreateReq{Feature: feature, Repos: []CreateRepoReq{{Key: "app"}}}
	if err := createFeature(context.Background(), cfg, req, newPortAllocator(), &memLog{}); err != nil {
		t.Fatalf("create %s: %v", feature, err)
	}
	f := findFeature(scanState(context.Background(), cfg), feature)
	if f == nil || len(f.Worktrees) != 1 {
		t.Fatalf("worktree %s não encontrada", feature)
	}
	return f.Worktrees[0]
}

func tarNames(t *testing.T, p string) []string {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var names []string
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	return names
}

func hasName(list []string, want string) bool {
	for _, n := range list {
		if n == want {
			return true
		}
	}
	return false
}

func TestSnapshotExcludesSecretsAndRestores(t *testing.T) {
	cfg := fixture(t)
	cfg.StateDir = t.TempDir()
	ctx := context.Background()
	wt := createForSnapshot(t, cfg, "snap1")

	write(t, filepath.Join(wt.Path, "yarn.lock"), "lock-alterado\n")
	write(t, filepath.Join(wt.Path, "novo.txt"), "conteúdo novo\n")
	write(t, filepath.Join(wt.Path, "id_rsa"), "PRIVATE KEY\n")
	write(t, filepath.Join(wt.Path, "notes/segredo.key"), "chave\n")

	lg := &memLog{}
	meta, err := createSnapshot(ctx, cfg, &cfg.Repos[0], wt, "manual", "teste", lg)
	if err != nil {
		t.Fatalf("snapshot: %v\n%s", err, lg)
	}
	if !meta.HasPatch || !meta.HasUntracked {
		t.Fatalf("faltou patch ou tar: %+v", meta)
	}
	for _, want := range []string{".env", "id_rsa", ".claude", "notes/segredo.key"} {
		if !hasName(meta.Omitted, want) {
			t.Fatalf("omitted não lista %q: %v", want, meta.Omitted)
		}
	}

	patch := read(t, filepath.Join(meta.Dir, "changes.patch"))
	if !strings.Contains(patch, "yarn.lock") || strings.Contains(patch, ".env") || strings.Contains(patch, "id_rsa") {
		t.Fatalf("patch inesperado:\n%s", patch)
	}
	names := tarNames(t, filepath.Join(meta.Dir, "untracked.tar"))
	if !hasName(names, "novo.txt") || hasName(names, ".env") || hasName(names, "id_rsa") {
		t.Fatalf("tar inesperado: %v", names)
	}

	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(meta.Dir); err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("pasta do snapshot: %v %v", fi, err)
		}
		for _, n := range []string{"meta.json", "changes.patch", "untracked.tar"} {
			if fi, err := os.Stat(filepath.Join(meta.Dir, n)); err != nil || fi.Mode().Perm() != 0o600 {
				t.Fatalf("%s: %v %v", n, fi, err)
			}
		}
	}

	// Volta a worktree ao estado limpo; segredos saem porque não são restaurados.
	run(t, wt.Path, "git", "checkout", "--", "yarn.lock")
	for _, n := range []string{"novo.txt", "id_rsa", "notes/segredo.key"} {
		if err := os.Remove(filepath.Join(wt.Path, n)); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Remove(filepath.Join(wt.Path, "notes"))
	if st, err := worktreeStatus(ctx, wt.Path); err != nil || st.Dirty != 0 {
		t.Fatalf("worktree não ficou limpa: %+v %v", st, err)
	}

	if err := restoreSnapshot(ctx, cfg, wt.Path, meta.Dir, &memLog{}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := read(t, filepath.Join(wt.Path, "yarn.lock")); got != "lock-alterado\n" {
		t.Fatalf("yarn.lock restaurado = %q", got)
	}
	if got := read(t, filepath.Join(wt.Path, "novo.txt")); got != "conteúdo novo\n" {
		t.Fatalf("novo.txt restaurado = %q", got)
	}
	if exists(filepath.Join(wt.Path, "id_rsa")) {
		t.Fatal("id_rsa não deveria ser restaurado")
	}

	// Worktree suja: recusa restaurar.
	write(t, filepath.Join(wt.Path, "yarn.lock"), "sujo\n")
	if err := restoreSnapshot(ctx, cfg, wt.Path, meta.Dir, &memLog{}); err == nil {
		t.Fatal("restauração em worktree suja deveria falhar")
	}
}

func TestSnapshotRetention(t *testing.T) {
	cfg := fixture(t)
	cfg.StateDir = t.TempDir()
	cfg.Snapshots = &SnapshotConfig{Keep: 2, MaxAgeDays: 30, PreRemoveDays: 90, MaxMB: 100, FileMaxMB: 20}
	ctx := context.Background()
	wt := createForSnapshot(t, cfg, "retencao")

	orig := snapshotNow
	defer func() { snapshotNow = orig }()
	base := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		snapshotNow = func() time.Time { return at }
		if _, err := createSnapshot(ctx, cfg, &cfg.Repos[0], wt, "manual", "", nil); err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
	}
	list := listSnapshots(cfg, "app", wt.Folder)
	if len(list) != 2 {
		t.Fatalf("retenção por quantidade: %d snapshots", len(list))
	}
	if !list[0].CreatedAt.Equal(base.Add(3*time.Minute)) || !list[1].CreatedAt.Equal(base.Add(2*time.Minute)) {
		t.Fatalf("manteve os errados: %v", list)
	}

	// pre-remove vive 90 dias, não 30.
	snapshotNow = func() time.Time { return base.Add(time.Hour) }
	pre, err := createSnapshot(ctx, cfg, &cfg.Repos[0], wt, "pre-remove", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshotNow = func() time.Time { return base.Add(40 * 24 * time.Hour) }
	if _, err := createSnapshot(ctx, cfg, &cfg.Repos[0], wt, "manual", "", nil); err != nil {
		t.Fatal(err)
	}
	list = listSnapshots(cfg, "app", wt.Folder)
	var keptPre bool
	for _, m := range list {
		if m.Dir == pre.Dir {
			keptPre = true
		}
	}
	if !keptPre {
		t.Fatalf("pre-remove de 40 dias não deveria expirar: %v", list)
	}
	for _, m := range list {
		if m.Reason == "manual" && m.CreatedAt.Before(base.Add(30*24*time.Hour)) {
			t.Fatalf("snapshot manual antigo deveria ter saído: %v", m.CreatedAt)
		}
	}
}

func TestSnapshotBundlesUnpushed(t *testing.T) {
	cfg := fixture(t)
	cfg.StateDir = t.TempDir()
	ctx := context.Background()
	wt := createForSnapshot(t, cfg, "bundle")

	write(t, filepath.Join(wt.Path, "commit-local.txt"), "x\n")
	run(t, wt.Path, "git", "add", "commit-local.txt")
	run(t, wt.Path, "git", "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false",
		"commit", "-q", "-m", "local")
	f := findFeature(scanState(ctx, cfg), "bundle")
	if f == nil || f.Worktrees[0].Unpushed != 1 {
		t.Fatalf("unpushed esperado 1: %+v", f)
	}
	meta, err := createSnapshot(ctx, cfg, &cfg.Repos[0], f.Worktrees[0], "manual", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(meta.Dir, "commits.bundle")
	if !meta.HasBundle || !exists(bundle) {
		t.Fatal("bundle não criado para commit fora do remoto")
	}
	if b := read(t, bundle); !strings.HasPrefix(b, "# v2 git bundle") {
		t.Fatalf("bundle inválido: %q", b[:20])
	}
}

func TestRemoveForceSnapshotsAndAborts(t *testing.T) {
	cfg := fixture(t)
	cfg.StateDir = t.TempDir()
	ctx := context.Background()
	lg := &memLog{}

	wt := createForSnapshot(t, cfg, "descartavel")
	write(t, filepath.Join(wt.Path, "sujo.txt"), "x\n")
	if err := removeFeature(ctx, cfg, &RemoveReq{Feature: "descartavel", Force: true}, lg); err != nil {
		t.Fatalf("remove forçado: %v\n%s", err, lg)
	}
	if isDir(wt.Path) {
		t.Fatal("worktree não removida")
	}
	list := listSnapshots(cfg, "app", wt.Folder)
	if len(list) != 1 || list[0].Reason != "pre-remove" {
		t.Fatalf("snapshot pré-remoção ausente: %+v", list)
	}

	// Com MaxMB=0, o snapshot falha e nada é removido.
	cfg.Snapshots = &SnapshotConfig{Keep: 10, MaxAgeDays: 30, PreRemoveDays: 90, MaxMB: 0, FileMaxMB: 20}
	wt2 := createForSnapshot(t, cfg, "nao-sai")
	write(t, filepath.Join(wt2.Path, "sujo.txt"), "x\n")
	err := removeFeature(ctx, cfg, &RemoveReq{Feature: "nao-sai", Force: true}, &memLog{})
	if err == nil {
		t.Fatal("remoção deveria abortar quando o snapshot falha")
	}
	if !isDir(wt2.Path) {
		t.Fatal("worktree foi removida apesar da falha do snapshot")
	}
	if !strings.Contains(err.Error(), "nada foi removido") {
		t.Fatalf("erro inesperado: %v", err)
	}
}

// Worktree trancada não sai nem com "forçar": o git recusa e o fallback que
// apaga a pasta direto não pode passar por cima do lock.
func TestRemoveRefusesLockedWorktree(t *testing.T) {
	cfg := fixture(t)
	cfg.StateDir = t.TempDir()
	ctx := context.Background()

	wt := createForSnapshot(t, cfg, "trancada")
	write(t, filepath.Join(wt.Path, "sujo.txt"), "x\n")
	if _, err := git(ctx, cfg.mainPath(cfg.repo("app")), "worktree", "lock", "--reason", "pendrive", wt.Path); err != nil {
		t.Fatal(err)
	}
	if f := findFeature(scanState(ctx, cfg), "trancada"); f == nil || !f.Worktrees[0].Locked {
		t.Fatal("scan não marcou a worktree como trancada")
	}
	lg := &memLog{}
	err := removeFeature(ctx, cfg, &RemoveReq{Feature: "trancada", Force: true, SkipSnapshot: true}, lg)
	if err == nil || !strings.Contains(err.Error(), "trancada") {
		t.Fatalf("remoção forçada deveria recusar worktree trancada: %v", err)
	}
	if !exists(filepath.Join(wt.Path, "sujo.txt")) {
		t.Fatal("arquivo da worktree trancada foi apagado")
	}

	// Lock posto depois do scan: removeWorktree recebe Locked=false, o git
	// recusa e a pasta precisa continuar lá.
	stale := wt
	stale.Locked = false
	if err := removeWorktree(ctx, cfg, cfg.repo("app"), stale, &RemoveReq{Force: true}, lg); err == nil {
		t.Fatal("removeWorktree deveria falhar com lock")
	}
	if !exists(filepath.Join(wt.Path, "sujo.txt")) {
		t.Fatal("fallback apagou a pasta de uma worktree trancada")
	}
}

// Agente vivo na worktree é risco: sem "forçar", nada sai.
func TestRemoveLiveAgentIsRisk(t *testing.T) {
	cfg := fixture(t)
	cfg.StateDir = t.TempDir()
	ctx := context.Background()

	wt := createForSnapshot(t, cfg, "com-agente")
	live := map[string]int{wt.Path: 1}
	err := removeFeature(ctx, cfg, &RemoveReq{Feature: "com-agente", Live: live}, &memLog{})
	if err == nil || !strings.Contains(err.Error(), "nada foi removido") {
		t.Fatalf("remoção com agente vivo deveria exigir forçar: %v", err)
	}
	if !isDir(wt.Path) {
		t.Fatal("worktree removida com agente vivo")
	}
	if err := removeFeature(ctx, cfg, &RemoveReq{Feature: "com-agente", Live: live, Force: true}, &memLog{}); err != nil {
		t.Fatalf("remoção forçada: %v", err)
	}
	if isDir(wt.Path) {
		t.Fatal("worktree não removida com forçar")
	}
}
