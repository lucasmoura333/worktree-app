//go:build windows

package main

import (
	"fmt"
	"os"
)

func runShell(args []string) {
	fmt.Fprintln(os.Stderr, "worktree-app run: só existe no Linux/WSL")
	os.Exit(1)
}
