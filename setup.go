package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// setupWorktree deixa uma worktree pronta para uso: arquivos que o git não
// versiona (.env etc.), .env do docker com nome/porta próprios e, se pedido,
// dependências instaladas. Cada passo pula o que já existe, então rodar de
// novo completa uma worktree pela metade.
func setupWorktree(ctx context.Context, cfg *Config, r *RepoConfig, feature, wt string, install bool, port int, ports *portAllocator, lg Logger) error {
	main := cfg.mainPath(r)
	copyUntracked(r, main, wt, lg)
	if err := patchDockerEnv(cfg, r, feature, main, wt, port, ports, lg); err != nil {
		return err
	}
	return installDeps(ctx, r, wt, install, lg)
}

// copyUntracked copia sem abrir nem registrar o conteúdo: no log vai só o nome.
func copyUntracked(r *RepoConfig, main, wt string, lg Logger) {
	for _, item := range r.Copy {
		rel := filepath.FromSlash(item)
		src, dst := filepath.Join(main, rel), filepath.Join(wt, rel)
		info, err := os.Stat(src)
		if err != nil {
			lg.Log(r.Key, "info", item+": não existe em "+r.Main+", pulado")
			continue
		}
		if exists(dst) {
			lg.Log(r.Key, "info", item+": já existe, mantido")
			continue
		}
		if info.IsDir() {
			err = copyTree(src, dst)
		} else {
			err = copyFile(src, dst)
		}
		if err != nil {
			lg.Log(r.Key, "warn", item+": falha ao copiar: "+err.Error())
			continue
		}
		lg.Log(r.Key, "ok", "copiado "+item)
	}
}

func patchDockerEnv(cfg *Config, r *RepoConfig, feature, main, wt string, port int, ports *portAllocator, lg Logger) error {
	d := r.DockerEnv
	if d == nil {
		return nil
	}
	dst := filepath.Join(wt, filepath.FromSlash(d.File))
	info, err := os.Stat(dst)
	if err != nil {
		lg.Log(r.Key, "warn", d.File+" ausente: "+d.ProjectKey+"/"+d.PortKey+" não ajustados")
		return nil
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		return err
	}
	content := string(b)
	srcProject := ""
	if sb, err := os.ReadFile(filepath.Join(main, filepath.FromSlash(d.File))); err == nil {
		srcProject, _ = getEnvValue(string(sb), d.ProjectKey)
	}
	if cur, _ := getEnvValue(content, d.ProjectKey); cur != "" && cur != srcProject {
		lg.Log(r.Key, "info", d.File+" já tem "+d.ProjectKey+" próprio, mantido")
		return nil
	}
	base := srcProject
	if base == "" {
		base = "worktree"
	}
	project := composeName(base + "-" + feature)
	if port == 0 {
		if port, err = ports.next(cfg, r, true); err != nil {
			return err
		}
	}
	content = setEnvValue(content, d.ProjectKey, project)
	content = setEnvValue(content, d.PortKey, strconv.Itoa(port))
	if err := os.WriteFile(dst, []byte(content), info.Mode().Perm()); err != nil {
		return err
	}
	lg.Log(r.Key, "ok", fmt.Sprintf("%s: %s=%s, %s=%d", d.File, d.ProjectKey, project, d.PortKey, port))
	return nil
}

// installDeps só roda quando pedido: por padrão a worktree nasce sem
// dependências e fica pronta na hora.
func installDeps(ctx context.Context, r *RepoConfig, wt string, install bool, lg Logger) error {
	if !install {
		return nil
	}
	var errs []error
	for _, d := range r.Deps {
		if exists(filepath.Join(wt, d.Dir)) {
			lg.Log(r.Key, "info", d.Dir+": já existe, mantido")
			continue
		}
		if len(d.Install) == 0 {
			lg.Log(r.Key, "warn", d.Dir+": sem comando de instalação configurado")
			continue
		}
		start := time.Now()
		lg.Log(r.Key, "info", d.Dir+": "+strings.Join(d.Install, " ")+"…")
		if err := runStreaming(ctx, wt, d.Install, r.Key, lg); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", d.Dir, err))
			lg.Log(r.Key, "error", d.Dir+": instalação falhou: "+err.Error())
			continue
		}
		lg.Log(r.Key, "ok", fmt.Sprintf("%s: instalado em %s", d.Dir, since(start)))
	}
	return errors.Join(errs...)
}

func since(t time.Time) string { return time.Since(t).Round(100 * time.Millisecond).String() }
