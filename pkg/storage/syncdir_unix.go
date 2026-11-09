//go:build !windows

package storage

import (
	"errors"
	"os"
)

// SyncDirectory makes preceding file creations, renames and removals durable.
func SyncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
