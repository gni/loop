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

func tryFlock(file *os.File, exclusive bool) (bool, error) {
	operation := syscall.LOCK_SH | syscall.LOCK_NB
	if exclusive {
		operation = syscall.LOCK_EX | syscall.LOCK_NB
	}
	err := syscall.Flock(int(file.Fd()), operation)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EINTR) {
		return false, nil
	}
	return err == nil, err
}

func funlock(file *os.File) error {
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}
