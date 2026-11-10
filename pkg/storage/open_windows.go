//go:build windows

package storage

import "os"

func openRead(path string, open func(string, int, os.FileMode) (*os.File, error)) (*os.File, error) {
	return open(path, os.O_RDONLY, 0)
}
