package packages

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"loop/pkg/config"
	"loop/pkg/storage"
)

func (m Manager) publishInstall(ctx context.Context, entries []Package, source, destination string) error {
	data, err := encodeRegistry(entries)
	if err != nil {
		return err
	}
	if source == "" {
		if err := ctx.Err(); err != nil {
			return err
		}
		return config.WritePrivateFile(filepath.Join(m.Directory, "packages.json"), data)
	}
	path := filepath.Join(m.Directory, "packages.json")
	previous, readErr := storage.ReadFile(path, maxRegistryBytes)
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Once publication starts, finish the registry update or roll back both
	// pieces without allowing caller cancellation to leave them inconsistent.
	return replaceDirectory(source, destination, func() error {
		if err := config.WritePrivateFile(path, data); err != nil {
			var restoreErr error
			if readErr == nil {
				restoreErr = config.WritePrivateFile(path, previous)
			} else {
				restoreErr = os.Remove(path)
				if os.IsNotExist(restoreErr) {
					restoreErr = nil
				}
				if restoreErr == nil {
					restoreErr = storage.SyncDirectory(m.Directory)
				}
			}
			if restoreErr != nil {
				restoreErr = fmt.Errorf("restore package registry: %w", restoreErr)
			}
			return errors.Join(err, restoreErr)
		}
		return nil
	})
}

func replaceDirectory(source, destination string, publish func() error) error {
	parent := filepath.Dir(destination)
	backup := ""
	if _, err := os.Lstat(destination); err == nil {
		var err error
		backup, err = os.MkdirTemp(parent, ".package-previous-*")
		if err != nil {
			return err
		}
		if err := os.Rename(destination, filepath.Join(backup, "content")); err != nil {
			return errors.Join(err, os.Remove(backup))
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	rollback := func() error {
		if backup != "" {
			if err := os.Rename(filepath.Join(backup, "content"), destination); err != nil {
				return fmt.Errorf("restore previous package from %s: %w", backup, err)
			}
			if err := os.Remove(backup); err != nil {
				return err
			}
		}
		return storage.SyncDirectory(parent)
	}
	if err := os.Rename(source, destination); err != nil {
		return errors.Join(err, rollback())
	}
	if err := publish(); err != nil {
		if removeErr := os.RemoveAll(destination); removeErr != nil {
			return errors.Join(err, fmt.Errorf("remove uncommitted package: %w", removeErr))
		}
		return errors.Join(err, rollback())
	}
	if err := storage.SyncDirectory(parent); err != nil {
		return fmt.Errorf("package installed, but directory sync failed: %w", err)
	}
	if backup != "" {
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("package installed, but previous package cleanup failed: %w", err)
		}
		if err := storage.SyncDirectory(parent); err != nil {
			return fmt.Errorf("package installed, but cleanup sync failed: %w", err)
		}
	}
	return nil
}
