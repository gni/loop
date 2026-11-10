//go:build !windows

package storage

import (
	"os"

	"golang.org/x/sys/unix"
)

func openRead(path string, open func(string, int, os.FileMode) (*os.File, error)) (*os.File, error) {
	return open(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
}
