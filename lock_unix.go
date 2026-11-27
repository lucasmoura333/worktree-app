//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"
)

// lockFile tranca o arquivo com flock. A trava some quando o processo morre,
// então um painel derrubado no meio não deixa a próxima instância presa.
func lockFile(p string) (func(), error) {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errOpBusy
		}
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}
