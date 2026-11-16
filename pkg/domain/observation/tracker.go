package observation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"loop/pkg/domain/diagnostic"
	"loop/pkg/storage"
)

type FileObservation struct {
	Path    string
	SHA256  string
	ModTime time.Time
	Size    int64
	ReadAt  time.Time
	Ranges  []LineRange
}

type LineRange struct {
	Start, End int
	Complete   bool
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

// Revision safety remains whole-file even when only a source window is shown.
func (fot *FileObservationTracker) RecordReadRange(path string, data []byte, start, end int, complete bool) {
	if fot == nil {
		return
	}
	hash := sha256.Sum256(data)
	revision := hex.EncodeToString(hash[:])
	fot.mu.Lock()
	defer fot.mu.Unlock()
	if fot.disabled {
		return
	}
	path = filepath.Clean(path)
	entry := fot.observations[path]
	if entry.SHA256 != revision {
		entry = FileObservation{Path: path, SHA256: revision, Size: int64(len(data))}
	}
	entry.ReadAt = time.Now()
	if info, err := os.Stat(path); err == nil {
		entry.ModTime = info.ModTime()
	}
	if start > 0 && end >= start {
		matched := false
		for i := range entry.Ranges {
			r := &entry.Ranges[i]
			if r.Complete == complete && start <= r.End+1 && end >= r.Start-1 {
				r.Start = min(r.Start, start)
				r.End = max(r.End, end)
				matched = true
				break
			}
		}
		if !matched {
			entry.Ranges = append(entry.Ranges, LineRange{start, end, complete})
		}
		if len(entry.Ranges) > 128 {
			entry.Ranges = append([]LineRange(nil), entry.Ranges[len(entry.Ranges)-128:]...)
		}
	}
	fot.observations[path] = entry
}

func (fot *FileObservationTracker) Snapshot(path string) (FileObservation, bool) {
	if fot == nil {
		return FileObservation{}, false
	}
	fot.mu.RLock()
	defer fot.mu.RUnlock()
	entry, ok := fot.observations[filepath.Clean(path)]
	entry.Ranges = append([]LineRange(nil), entry.Ranges...)
	return entry, ok
}

// InvalidateCoverage keeps the safety revision while dropping unconfirmed ranges
// if hooks or final output pruning no longer retain the original source page.
func (fot *FileObservationTracker) InvalidateCoverage(path string) {
	if fot == nil {
		return
	}
	fot.mu.Lock()
	defer fot.mu.Unlock()
	path = filepath.Clean(path)
	if entry, ok := fot.observations[path]; ok {
		entry.Ranges = nil
		fot.observations[path] = entry
	}
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
				return &diagnostic.Error{Code: diagnostic.MissingPath, Target: cleanPath, Cause: fmt.Errorf("cannot edit '%s': file does not exist on disk. Locate the intended existing file before retrying", base)}
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
		return &diagnostic.Error{Code: diagnostic.ReadRequired, Target: cleanPath, Cause: fmt.Errorf("cannot modify '%s': file has not been read in this session. You must call 'read' with path '%s' first to inspect its contents before editing or overwriting", base, cleanPath)}
	}

	if fi.Size() != obs.Size {
		return &diagnostic.Error{Code: diagnostic.RevisionChanged, Target: cleanPath, Cause: fmt.Errorf("cannot modify '%s': file size changed since your last read; read '%s' again", base, cleanPath)}
	}
	if fi.Size() > 20*1024*1024 {
		if !fi.ModTime().Equal(obs.ModTime) {
			return &diagnostic.Error{Code: diagnostic.RevisionChanged, Target: cleanPath, Cause: fmt.Errorf("cannot modify '%s': large file changed since your last read; read '%s' again", base, cleanPath)}
		}
		return nil
	}
	diskData, err := storage.ReadFile(cleanPath, 20<<20)
	if err != nil {
		return fmt.Errorf("cannot verify file '%s' before mutation: %w", base, err)
	}
	diskHash := sha256.Sum256(diskData)
	if hex.EncodeToString(diskHash[:]) != obs.SHA256 {
		return &diagnostic.Error{Code: diagnostic.RevisionChanged, Target: cleanPath, Cause: fmt.Errorf("cannot modify '%s': file was modified on disk since your last read (SHA256 mismatch). Please call 'read' with path '%s' first to view the latest contents before making modifications", base, cleanPath)}
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
