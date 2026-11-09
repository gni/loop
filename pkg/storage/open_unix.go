//go:build !windows

package storage

import (
	"os"

	"golang.org/x/sys/unix"
)

func openRead(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
}
