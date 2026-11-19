//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideWindow evita que cada git/robocopy abra uma janela de console.
func hideWindow(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
