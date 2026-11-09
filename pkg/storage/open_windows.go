//go:build windows

package storage

import "os"

func openRead(path string) (*os.File, error) { return os.Open(path) }
