package storage

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func Lock(path string, exclusive bool) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	operation := syscall.LOCK_SH
	if exclusive {
		operation = syscall.LOCK_EX
	}
	for {
		err = syscall.Flock(int(file.Fd()), operation)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() {
		for {
			unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
			if !errors.Is(unlockErr, syscall.EINTR) {
				break
			}
		}
		_ = file.Close()
	}, nil
}
