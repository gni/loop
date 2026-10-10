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

// FileObservation holds the content hash and timestamps of a file inspected during the session.
type FileObservation struct {
	Path    string
	SHA256  string
	ModTime time.Time
	ReadAt  time.Time
}

// FileObservationTracker tracks files inspected during the session and enforces
// read-before-edit and compare-and-swap (CAS) freshness guarantees.
type FileObservationTracker struct {
	mu           sync.RWMutex
	observations map[string]FileObservation
	disabled     bool
}

// NewFileObservationTracker creates a new thread-safe file observation registry.
func NewFileObservationTracker() *FileObservationTracker {
	return &FileObservationTracker{
		observations: make(map[string]FileObservation),
	}
}

// SetDisabled enables or disables observation policy enforcement.
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
	hashStr := hex.EncodeToString(hash[:])

	modTime := time.Now()
	if fi, err := os.Stat(cleanPath); err == nil {
		modTime = fi.ModTime()
	}

	fot.observations[cleanPath] = FileObservation{
		Path:    cleanPath,
		SHA256:  hashStr,
		ModTime: modTime,
		ReadAt:  time.Now(),
	}
}

// RecordRead records that the agent has inspected the content of absPath.
func (fot *FileObservationTracker) RecordRead(absPath string, data []byte) {
	fot.recordFile(absPath, data)
}

// CheckMutationAllowed verifies that a mutation (write/edit) adheres to the read-before-edit
// and CAS freshness policy.
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
	displayPath := cleanPath
	base := filepath.Base(cleanPath)

	fi, err := os.Stat(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			if isEdit {
				return fmt.Errorf("cannot edit '%s': file does not exist on disk. Call 'write' to create new files", base)
			}
			// Writing a brand new non-existent file is always allowed
			return nil
		}
		// Unreadable stat (permissions, etc) -> let provider fail naturally
		return nil
	}

	// File exists on disk
	obs, exists := fot.observations[cleanPath]
	if !exists {
		// Existing file has NOT been read in this session
		return fmt.Errorf("cannot modify '%s': file has not been read in this session. You must call 'read' with path '%s' first to inspect its contents before editing or overwriting", base, displayPath)
	}

	// File was read, check freshness (Compare-And-Swap)
	if fi.Size() <= 20*1024*1024 { // Only hash files <= 20MB
		diskData, err := os.ReadFile(cleanPath)
		if err == nil {
			diskHash := sha256.Sum256(diskData)
			diskHashStr := hex.EncodeToString(diskHash[:])
			if diskHashStr != obs.SHA256 {
				return fmt.Errorf("cannot modify '%s': file was modified on disk since your last read (SHA256 mismatch). Please call 'read' with path '%s' first to view the latest contents before making modifications", base, displayPath)
			}
		}
	}

	return nil
}

// RecordMutation updates the observation record after a successful write or edit.
func (fot *FileObservationTracker) RecordMutation(absPath string, data []byte) {
	fot.recordFile(absPath, data)
}

// FileObserver is an optional interface implemented by AgentContext to enforce
// read-before-edit and CAS policies on file operations.
type FileObserver = domaintool.FileObserver
