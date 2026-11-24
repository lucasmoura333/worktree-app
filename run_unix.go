//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// runShell é o subcomando "run": dentro da aba nova, roda o agente pelo
// shell de login do usuário (PATH completo) e, quando ele sai, deixa o
// shell aberto na mesma pasta.
func runShell(args []string) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	if len(args) > 0 {
		quoted := make([]string, len(args))
		for i, a := range args {
			if !argRe.MatchString(a) {
				fmt.Fprintf(os.Stderr, "worktree-app run: argumento recusado: %q\n", a)
				os.Exit(2)
			}
			quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		cmd := exec.Command(shell, "-lic", strings.Join(quoted, " "))
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		_ = cmd.Run()
	}
	err := syscall.Exec(shell, []string{shell, "-l"}, os.Environ())
	fmt.Fprintln(os.Stderr, "worktree-app run:", err)
	os.Exit(1)
}
