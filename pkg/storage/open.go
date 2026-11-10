package storage

import (
	"fmt"
	"os"
)

// OpenRegularFile opens a file for reading and verifies the opened object.
// On Unix, opening a FIFO without a writer must not block this check.
func OpenRegularFile(path string) (*os.File, error) {
	return openRegularFile(path, os.OpenFile)
}

func openRegularFile(path string, open func(string, int, os.FileMode) (*os.File, error)) (*os.File, error) {
	file, err := openRead(path, open)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file", path)
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
