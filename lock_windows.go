//go:build windows

package main

import (
	"errors"
	"syscall"
)

const errSharingViolation syscall.Errno = 32

// lockFile abre o arquivo sem compartilhamento: enquanto o handle existir,
// outra instância não consegue abrir. O Windows fecha o handle se o processo
// morrer.
func lockFile(p string) (func(), error) {
	name, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, errSharingViolation) {
			return nil, errOpBusy
		}
		return nil, err
	}
	return func() { _ = syscall.CloseHandle(h) }, nil
}
