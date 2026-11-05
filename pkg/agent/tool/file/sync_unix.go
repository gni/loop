//go:build !windows

package file

import (
	"errors"
	"fmt"
	"os"
)

func syncParentDirectory(dir string) error {
	parent, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open parent directory for durability: %w", err)
	}
	if err := errors.Join(parent.Sync(), parent.Close()); err != nil {
		return fmt.Errorf("sync parent directory: %w", err)
	}
	return nil
}
