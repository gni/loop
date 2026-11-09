package config

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"loop/pkg/storage"
)

const configJournalName = ".config-recovery.json"
const maxConfigJournalBytes = 64 << 20

type configUndoFile struct {
	Name     string `json:"name"`
	Existed  bool   `json:"existed"`
	Data     []byte `json:"data,omitempty"`
	Checksum string `json:"sha256,omitempty"`
}

type configUndoJournal struct {
	Version int              `json:"version"`
	Files   []configUndoFile `json:"files"`
}

func validConfigMainName(name string) bool {
	if name == "" || name == "." || name == ".." || !filepath.IsLocal(name) || filepath.Base(name) != name {
		return false
	}
	if strings.ContainsAny(name, "/\\:") || strings.TrimRight(name, ". ") != name {
		return false
	}
	switch strings.ToLower(name) {
	case "mcp.json", "providers.json", ".config.lock", configJournalName:
		return false
	}
	return true
}

// The caller holds the exclusive configuration lock throughout the transaction.
func prepareConfigJournal(dir string, files []configFileUpdate) error {
	journal := configUndoJournal{Version: 1, Files: make([]configUndoFile, len(files))}
	for i, file := range files {
		journal.Files[i] = configUndoFile{Name: filepath.Base(file.path), Existed: file.existed, Data: file.previous}
		if file.existed {
			journal.Files[i].Checksum = fmt.Sprintf("%x", sha256.Sum256(file.previous))
		}
	}
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	if len(data) > maxConfigJournalBytes {
		return fmt.Errorf("configuration recovery journal exceeds 64 MiB")
	}
	if err := writePrivateConfigFile(filepath.Join(dir, configJournalName), data); err != nil {
		return err
	}
	return storage.SyncDirectory(dir)
}

func removeConfigJournal(dir string) error {
	if err := os.Remove(filepath.Join(dir, configJournalName)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return storage.SyncDirectory(dir)
}

// Restore every file, not just the last successful replacement. Recovery may
// itself be interrupted; keeping the journal makes the next attempt idempotent.
func recoverConfigBundleLocked(dir string) error {
	data, err := storage.ReadFile(filepath.Join(dir, configJournalName), maxConfigJournalBytes)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read configuration recovery journal: %w", err)
	}
	var journal configUndoJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return fmt.Errorf("invalid configuration recovery journal: %w", err)
	}
	if journal.Version != 1 || len(journal.Files) != 3 || !validConfigMainName(journal.Files[0].Name) || journal.Files[1].Name != "mcp.json" || journal.Files[2].Name != "providers.json" {
		return fmt.Errorf("invalid configuration recovery journal layout")
	}
	// Validate the complete journal before changing any target.
	for i, file := range journal.Files {
		limit := 16 << 20
		if i == 0 {
			limit = 8 << 20
		}
		if len(file.Data) > limit || (!file.Existed && (len(file.Data) > 0 || file.Checksum != "")) {
			return fmt.Errorf("invalid recovery data for %s", file.Name)
		}
		if file.Existed && file.Checksum != fmt.Sprintf("%x", sha256.Sum256(file.Data)) {
			return fmt.Errorf("configuration recovery checksum mismatch for %s", file.Name)
		}
	}
	var failures []error
	for _, file := range journal.Files {
		path := filepath.Join(dir, file.Name)
		var err error
		if file.Existed {
			err = writePrivateConfigFile(path, file.Data)
		} else {
			info, statErr := os.Lstat(path)
			if os.IsNotExist(statErr) {
				continue
			}
			if statErr != nil {
				err = statErr
			} else if info.IsDir() {
				err = fmt.Errorf("refusing to remove a directory during recovery")
			} else {
				err = os.Remove(path)
			}
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("restore %s: %w", file.Name, err))
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	if err := storage.SyncDirectory(dir); err != nil {
		return fmt.Errorf("sync recovered configuration: %w", err)
	}
	if err := removeConfigJournal(dir); err != nil {
		return fmt.Errorf("finish configuration recovery: %w", err)
	}
	return nil
}
