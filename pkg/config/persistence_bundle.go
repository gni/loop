package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"loop/pkg/storage"
)

type configFileUpdate struct {
	path     string
	data     []byte
	previous []byte
	existed  bool
}

// Bundle writers and recovery share one exclusive file lock.
var errConfigExists = errors.New("configuration was created concurrently")

func writeConfigBundle(ctx context.Context, files []configFileUpdate, createOnly bool) error {
	if len(files) == 0 {
		return nil
	}
	release, err := storage.LockContext(ctx, filepath.Join(filepath.Dir(files[0].path), ".config.lock"), true)
	if err != nil {
		return fmt.Errorf("lock configuration: %w", err)
	}
	defer release()
	dir := filepath.Dir(files[0].path)
	if err := recoverConfigBundleLocked(dir); err != nil {
		return err
	}
	for i := range files {
		limit := 16 << 20
		if i == 0 {
			limit = 8 << 20
		}
		if len(files[i].data) > limit {
			return fmt.Errorf("configuration file %s exceeds %d MiB", filepath.Base(files[i].path), limit>>20)
		}
		data, err := storage.ReadFile(files[i].path, int64(limit))
		if err == nil {
			files[i].previous = data
			files[i].existed = true
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("read configuration before save: %w", err)
		}
	}
	if createOnly && files[0].existed {
		return errConfigExists
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := prepareConfigJournal(dir, files); err != nil {
		return fmt.Errorf("prepare configuration recovery: %w", err)
	}
	for _, file := range files {
		if err := writePrivateConfigFile(file.path, file.data); err != nil {
			return errors.Join(fmt.Errorf("save %s: %w", filepath.Base(file.path), err), recoverConfigBundleLocked(dir))
		}
	}
	if err := storage.SyncDirectory(dir); err != nil {
		return errors.Join(fmt.Errorf("sync configuration bundle: %w", err), recoverConfigBundleLocked(dir))
	}
	if err := removeConfigJournal(dir); err != nil {
		return fmt.Errorf("configuration written, but recovery journal finalization failed: %w", err)
	}
	return nil
}

type configFileRead struct {
	data []byte
	err  error
}
type configSnapshot struct{ settings, mcp, providers configFileRead }

// Readers take one snapshot under the same lock as bundle writers, so a
// running process cannot load settings from different completed saves.
func readConfigSnapshot(ctx context.Context, path string) (configSnapshot, error) {
	dir := filepath.Dir(path)
	release, err := storage.LockContext(ctx, filepath.Join(dir, ".config.lock"), true)
	if err != nil {
		return configSnapshot{}, fmt.Errorf("lock configuration: %w", err)
	}
	defer release()
	if err := recoverConfigBundleLocked(dir); err != nil {
		return configSnapshot{}, err
	}
	var snapshot configSnapshot
	snapshot.settings.data, snapshot.settings.err = storage.ReadFile(path, 8<<20)
	snapshot.mcp.data, snapshot.mcp.err = storage.ReadFile(filepath.Join(dir, "mcp.json"), 16<<20)
	snapshot.providers.data, snapshot.providers.err = storage.ReadFile(filepath.Join(dir, "providers.json"), 16<<20)
	return snapshot, nil
}

// Migration helpers only create missing sidecar files. A concurrent completed
// save must never be overwritten with values from an earlier read snapshot.
func writeInitialConfigFile(ctx context.Context, path string, data []byte) error {
	if len(data) > 16<<20 {
		return fmt.Errorf("configuration file %s exceeds 16 MiB", filepath.Base(path))
	}
	release, err := storage.LockContext(ctx, filepath.Join(filepath.Dir(path), ".config.lock"), true)
	if err != nil {
		return err
	}
	defer release()
	if err := recoverConfigBundleLocked(filepath.Dir(path)); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writePrivateConfigFile(path, data)
}
