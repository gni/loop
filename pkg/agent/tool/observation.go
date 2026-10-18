package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	domaintool "loop/pkg/domain/tool"
)

type FileObservation struct {
	Path    string
	SHA256  string
	ModTime time.Time
	Size    int64
	ReadAt  time.Time
}

type FileObservationTracker struct {
	mu           sync.RWMutex
	observations map[string]FileObservation
	disabled     bool
}

func NewFileObservationTracker() *FileObservationTracker {
	return &FileObservationTracker{
		observations: make(map[string]FileObservation),
	}
}

func (fot *FileObservationTracker) SetDisabled(disabled bool) {
	if fot == nil {
		return
	}
	fot.mu.Lock()
	defer fot.mu.Unlock()
	if fot.disabled == disabled {
		return
	}
	fot.disabled = disabled
	fot.observations = make(map[string]FileObservation)
}

func (fot *FileObservationTracker) recordFile(absPath string, data []byte) {
	if fot == nil {
		return
	}
	fot.mu.Lock()
	defer fot.mu.Unlock()
	if fot.disabled {
		return
	}

	cleanPath := filepath.Clean(absPath)
	hash := sha256.Sum256(data)

	modTime := time.Now()
	size := int64(len(data))
	if fi, err := os.Stat(cleanPath); err == nil {
		modTime = fi.ModTime()
		size = fi.Size()
	}

	fot.observations[cleanPath] = FileObservation{
		Path:    cleanPath,
		SHA256:  hex.EncodeToString(hash[:]),
		ModTime: modTime,
		Size:    size,
		ReadAt:  time.Now(),
	}
}

func (fot *FileObservationTracker) RecordRead(absPath string, data []byte) {
	fot.recordFile(absPath, data)
}

func (fot *FileObservationTracker) CheckMutationAllowed(absPath string, isEdit bool) error {
	if fot == nil {
		return nil
	}
	fot.mu.RLock()
	defer fot.mu.RUnlock()
	if fot.disabled {
		return nil
	}

	cleanPath := filepath.Clean(absPath)
	base := filepath.Base(cleanPath)

	fi, err := os.Stat(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			if isEdit {
				return fmt.Errorf("cannot edit '%s': file does not exist on disk. Call 'write' to create new files", base)
			}
			return nil
		}
		return fmt.Errorf("cannot inspect '%s' before mutation: %w", base, err)
	}

	if !fi.Mode().IsRegular() {
		return fmt.Errorf("cannot modify '%s': target is not a regular file", base)
	}

	obs, exists := fot.observations[cleanPath]
	if !exists {
		return fmt.Errorf("cannot modify '%s': file has not been read in this session. You must call 'read' with path '%s' first to inspect its contents before editing or overwriting", base, cleanPath)
	}

	if fi.Size() != obs.Size {
		return fmt.Errorf("cannot modify '%s': file size changed since your last read; read '%s' again", base, cleanPath)
	}
	if fi.Size() > 20*1024*1024 {
		if !fi.ModTime().Equal(obs.ModTime) {
			return fmt.Errorf("cannot modify '%s': large file changed since your last read; read '%s' again", base, cleanPath)
		}
		return nil
	}
	diskData, err := os.ReadFile(cleanPath)
	if err != nil {
		return fmt.Errorf("cannot verify file '%s' before mutation: %w", base, err)
	}
	diskHash := sha256.Sum256(diskData)
	if hex.EncodeToString(diskHash[:]) != obs.SHA256 {
		return fmt.Errorf("cannot modify '%s': file was modified on disk since your last read (SHA256 mismatch). Please call 'read' with path '%s' first to view the latest contents before making modifications", base, cleanPath)
	}

	return nil
}

func (fot *FileObservationTracker) RecordMutation(absPath string, data []byte) {
	fot.recordFile(absPath, data)
}

func (fot *FileObservationTracker) Forget(absPath string) {
	if fot == nil {
		return
	}
	if absPath == "" {
		return
	}
	cleanPath := filepath.Clean(absPath)
	fot.mu.Lock()
	defer fot.mu.Unlock()
	if !fot.disabled {
		delete(fot.observations, cleanPath)
	}
}

func (fot *FileObservationTracker) ForgetAll() {
	if fot == nil {
		return
	}
	fot.mu.Lock()
	defer fot.mu.Unlock()
	if !fot.disabled {
		fot.observations = make(map[string]FileObservation)
	}
}

type FileObserver = domaintool.FileObserver
