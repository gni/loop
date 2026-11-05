package storage

import (
	"os"
	"path/filepath"
)

func Lock(path string, exclusive bool) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := flock(file, exclusive); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() {
		_ = funlock(file)
		_ = file.Close()
	}, nil
}
