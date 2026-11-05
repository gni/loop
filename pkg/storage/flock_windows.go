//go:build windows

package storage

import (
	"os"

	"golang.org/x/sys/windows"
)

func flock(file *os.File, exclusive bool) error {
	var flags uint32
	if exclusive {
		flags = windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, &overlapped)
}

func funlock(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}
