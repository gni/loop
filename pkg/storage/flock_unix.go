//go:build !windows

package storage

import (
	"errors"
	"os"
	"syscall"
)

func flock(file *os.File, exclusive bool) error {
	operation := syscall.LOCK_SH
	if exclusive {
		operation = syscall.LOCK_EX
	}
	for {
		err := syscall.Flock(int(file.Fd()), operation)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

func funlock(file *os.File) error {
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}
