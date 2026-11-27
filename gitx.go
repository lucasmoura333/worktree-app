package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func command(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	// Sem terminal para responder, um prompt de credencial travaria a operação.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	hideWindow(cmd)
	return cmd
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	var out, errb bytes.Buffer
	cmd := command(ctx, dir, "git", args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return out.String(), nil
}

// runStreaming executa um comando mandando cada linha de saída para o log.
func runStreaming(ctx context.Context, dir string, argv []string, repo string, lg Logger) error {
	cmd := command(ctx, dir, argv[0], argv[1:]...)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			if t := strings.TrimSpace(sc.Text()); t != "" {
				lg.Log(repo, "out", t)
			}
		}
		_, _ = io.Copy(io.Discard, pr)
		close(done)
	}()
	err := cmd.Wait()
	_ = pw.Close()
	<-done
	return err
}

func refExists(ctx context.Context, dir, ref string) bool {
	_, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

// branchRefOK confere o nome já composto pelo branchPattern (ex.: "dev.lock").
func branchRefOK(ctx context.Context, dir, name string) bool {
	if name == "" || strings.HasPrefix(name, "-") {
		return false
	}
	_, err := git(ctx, dir, "check-ref-format", "refs/heads/"+name)
	return err == nil
}

type gitWorktree struct {
	Path     string
	Head     string
	Branch   string
	Bare     bool
	Detached bool
	Prunable bool
	Locked   bool
}

func listWorktrees(ctx context.Context, dir string) ([]gitWorktree, error) {
	out, err := git(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var list []gitWorktree
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		key, val, _ := strings.Cut(strings.TrimRight(sc.Text(), "\r"), " ")
		if key == "worktree" {
			list = append(list, gitWorktree{Path: filepath.Clean(val)})
			continue
		}
		if len(list) == 0 {
			continue
		}
		cur := &list[len(list)-1]
		switch key {
		case "HEAD":
			cur.Head = val
		case "branch":
			cur.Branch = strings.TrimPrefix(val, "refs/heads/")
		case "bare":
			cur.Bare = true
		case "detached":
			cur.Detached = true
		case "prunable":
			cur.Prunable = true
		case "locked":
			cur.Locked = true
		}
	}
	return list, nil
}

type Branch struct {
	Name   string `json:"name"`
	Remote bool   `json:"remote"`
	Date   string `json:"date"`
}

func listBranches(ctx context.Context, dir string) ([]Branch, error) {
	out, err := git(ctx, dir, "for-each-ref", "--sort=-committerdate",
		"--format=%(refname)%09%(committerdate:short)", "refs/heads", "refs/remotes/origin")
	if err != nil {
		return nil, err
	}
	var list []Branch
	for _, line := range strings.Split(out, "\n") {
		ref, date, _ := strings.Cut(strings.TrimSpace(line), "\t")
		switch {
		case ref == "" || strings.HasSuffix(ref, "/HEAD"):
		case strings.HasPrefix(ref, "refs/heads/"):
			list = append(list, Branch{Name: strings.TrimPrefix(ref, "refs/heads/"), Date: date})
		case strings.HasPrefix(ref, "refs/remotes/"):
			list = append(list, Branch{Name: strings.TrimPrefix(ref, "refs/remotes/"), Remote: true, Date: date})
		}
	}
	return list, nil
}

type wtStatus struct {
	Dirty     int
	Untracked []string // caminhos "? " do status, já contados em Dirty
	Upstream  string
	Ahead     int
	Behind    int
}

func worktreeStatus(ctx context.Context, dir string) (wtStatus, error) {
	var st wtStatus
	out, err := git(ctx, dir, "status", "--porcelain=v2", "--branch")
	if err != nil {
		return st, err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case line == "":
		case strings.HasPrefix(line, "# branch.upstream "):
			st.Upstream = strings.TrimPrefix(line, "# branch.upstream ")
		case strings.HasPrefix(line, "# branch.ab "):
			f := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
			if len(f) == 2 {
				st.Ahead, _ = strconv.Atoi(strings.TrimPrefix(f[0], "+"))
				st.Behind, _ = strconv.Atoi(strings.TrimPrefix(f[1], "-"))
			}
		case strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "? "):
			st.Untracked = append(st.Untracked, strings.TrimSuffix(strings.TrimPrefix(line, "? "), "/"))
			st.Dirty++
		default:
			st.Dirty++
		}
	}
	return st, nil
}

// unpushedCount conta commits do HEAD que não existem em nenhum remoto; -1
// quando não dá para saber, o que a remoção trata como risco.
func unpushedCount(ctx context.Context, dir string) int {
	out, err := git(ctx, dir, "rev-list", "--count", "HEAD", "--not", "--remotes")
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return -1
	}
	return n
}
