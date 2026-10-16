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
	fot.disabled = disabled
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
	if fi, err := os.Stat(cleanPath); err == nil {
		modTime = fi.ModTime()
	}

	fot.observations[cleanPath] = FileObservation{
		Path:    cleanPath,
		SHA256:  hex.EncodeToString(hash[:]),
		ModTime: modTime,
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
		return nil
	}

	obs, exists := fot.observations[cleanPath]
	if !exists {
		return fmt.Errorf("cannot modify '%s': file has not been read in this session. You must call 'read' with path '%s' first to inspect its contents before editing or overwriting", base, cleanPath)
	}

	if fi.Size() <= 20*1024*1024 {
		if diskData, err := os.ReadFile(cleanPath); err == nil {
			diskHash := sha256.Sum256(diskData)
			if hex.EncodeToString(diskHash[:]) != obs.SHA256 {
				return fmt.Errorf("cannot modify '%s': file was modified on disk since your last read (SHA256 mismatch). Please call 'read' with path '%s' first to view the latest contents before making modifications", base, cleanPath)
			}
		}
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
	cleanPath := filepath.Clean(absPath)
	if cleanPath == "" {
		return
	}
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