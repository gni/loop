package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"

	"loop/pkg/agent/tool"
	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/storage"
)

const maxSnapshotBytes = 64 << 20
const maxSnapshotFiles = 4096

type rollbackJournal struct {
	Version   int                        `json:"version"`
	Workspace string                     `json:"workspace"`
	Snapshots map[string]journalSnapshot `json:"snapshots"`
}

type journalSnapshot struct {
	FileSnapshot
	Blob string `json:"blob,omitempty"`
}

func snapshotHash(data []byte, mode os.FileMode) string {
	h := sha256.New()
	fmt.Fprintf(h, "%o\x00", mode.Perm())
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func normalizeSnapshotPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(path)
}

// bindRecovery loads a session's private recovery journal without creating a file.
func (gc *GuidedController) bindRecovery(ctx context.Context, path, workspace string) error {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	if gc.journalPath == path && gc.workspace == workspace {
		return gc.journalErr
	}
	gc.journalPath, gc.workspace = path, workspace
	gc.journalHash, gc.journalErr = "", nil
	gc.journalBlobs = make(map[string]bool)
	gc.snapshots = make(map[string]FileSnapshot)
	if path == "" {
		return nil
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	}
	unlock, err := storage.LockContext(ctx, path+".lock", false)
	if err != nil {
		gc.journalErr = err
		return err
	}
	defer unlock()
	data, err := storage.ReadFile(path, maxSnapshotBytes*2)
	if os.IsNotExist(err) {
		return nil
	}
	if err == nil {
		var journal rollbackJournal
		err = json.Unmarshal(data, &journal)
		if err == nil && ((journal.Version != 1 && journal.Version != 2) || journal.Workspace != workspace) {
			err = fmt.Errorf("invalid rollback journal identity")
		}
		var total int
		loaded := make(map[string]FileSnapshot, len(journal.Snapshots))
		for key, record := range journal.Snapshots {
			snap := record.FileSnapshot
			if record.Blob != "" {
				decoded, decodeErr := hex.DecodeString(record.Blob)
				if decodeErr != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != record.Blob {
					err = fmt.Errorf("invalid rollback blob")
					break
				}
				snap.Data, err = storage.ReadFile(filepath.Join(path+".d", record.Blob), maxSnapshotBytes-int64(total))
				if err != nil {
					break
				}
				sum := sha256.Sum256(snap.Data)
				if hex.EncodeToString(sum[:]) != record.Blob {
					err = fmt.Errorf("rollback blob checksum mismatch")
					break
				}
				gc.journalBlobs[record.Blob] = true
			} else if journal.Version == 2 && snap.Existed {
				err = fmt.Errorf("missing rollback blob")
				break
			}
			total += len(snap.Data)
			if key != normalizeSnapshotPath(key) || key != snap.AbsPath || !filepath.IsAbs(snap.Target) || snap.Expected == "" || total > maxSnapshotBytes || len(journal.Snapshots) > maxSnapshotFiles {
				err = fmt.Errorf("invalid rollback snapshot")
				break
			}
			loaded[key] = snap
		}
		if err == nil {
			gc.snapshots = loaded
			gc.journalHash = snapshotHash(data, 0)
		}
	}
	if err != nil {
		gc.journalErr = fmt.Errorf("load rollback recovery: %w", err)
	}
	return gc.journalErr
}

// lockJournal rejects stale writers instead of losing another process's recovery.
func (gc *GuidedController) lockJournal(ctx context.Context) (func(), error) {
	if gc.journalErr != nil {
		return nil, gc.journalErr
	}
	if gc.journalPath == "" {
		return func() {}, nil
	}
	unlock, err := storage.LockContext(ctx, gc.journalPath+".lock", true)
	if err != nil {
		return nil, err
	}
	current, err := storage.ReadFile(gc.journalPath, maxSnapshotBytes*2)
	if err != nil && !os.IsNotExist(err) {
		unlock()
		return nil, err
	}
	hash := ""
	if err == nil {
		hash = snapshotHash(current, 0)
	}
	if hash != gc.journalHash {
		unlock()
		return nil, fmt.Errorf("rollback recovery changed in another process; reopen the session before editing")
	}
	return unlock, nil
}

func (gc *GuidedController) lockRecoveryFile(ctx context.Context, path string) (func(), error) {
	if gc.journalPath == "" {
		return func() {}, nil
	}
	sum := sha256.Sum256([]byte(path))
	return storage.LockContext(ctx, filepath.Join(filepath.Dir(gc.journalPath), ".files", hex.EncodeToString(sum[:])+".lock"), true)
}

func (gc *GuidedController) writeJournalLocked() error {
	if gc.journalPath == "" {
		return nil
	}
	if len(gc.snapshots) == 0 {
		if err := os.Remove(gc.journalPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		gc.journalHash = ""
		if err := storage.SyncDirectory(filepath.Dir(gc.journalPath)); err != nil {
			return err
		}
		gc.journalBlobs = make(map[string]bool)
		return os.RemoveAll(gc.journalPath + ".d")
	}
	// Immutable originals are written once. Subsequent edits only replace a
	// small manifest instead of repeatedly encoding and syncing every file body.
	records := make(map[string]journalSnapshot, len(gc.snapshots))
	used := make(map[string]bool, len(gc.snapshots))
	for key, snap := range gc.snapshots {
		record := journalSnapshot{FileSnapshot: snap}
		if snap.Existed {
			sum := sha256.Sum256(snap.Data)
			record.Blob = hex.EncodeToString(sum[:])
			blobPath := filepath.Join(gc.journalPath+".d", record.Blob)
			info, statErr := os.Lstat(blobPath)
			if !gc.journalBlobs[record.Blob] || statErr != nil || !info.Mode().IsRegular() || info.Size() != int64(len(snap.Data)) {
				if err := os.MkdirAll(gc.journalPath+".d", 0700); err != nil {
					return err
				}
				if err := filetool.AtomicWriteFile(blobPath, snap.Data, 0600); err != nil {
					return err
				}
				if err := storage.SyncDirectory(filepath.Dir(gc.journalPath)); err != nil {
					return err
				}
			}
			used[record.Blob] = true
		}
		record.Data = nil
		records[key] = record
	}
	data, err := json.Marshal(rollbackJournal{Version: 2, Workspace: gc.workspace, Snapshots: records})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(gc.journalPath), 0700); err != nil {
		return err
	}
	if err := filetool.AtomicWriteFile(gc.journalPath, data, 0600); err != nil {
		return err
	}
	gc.journalHash = snapshotHash(data, 0)
	gc.journalBlobs = used
	// The committed manifest no longer needs originals replaced after a re-read.
	if entries, err := os.ReadDir(gc.journalPath + ".d"); err == nil {
		for _, entry := range entries {
			if !used[entry.Name()] && !entry.IsDir() {
				_ = os.Remove(filepath.Join(gc.journalPath+".d", entry.Name()))
			}
		}
	}
	return nil
}

func (gc *GuidedController) HasSnapshot(path string) bool {
	if gc == nil || path == "" {
		return false
	}
	gc.mu.RLock()
	defer gc.mu.RUnlock()
	_, ok := gc.snapshots[normalizeSnapshotPath(path)]
	return ok
}

func (gc *GuidedController) CaptureFileSnapshot(path string) error {
	return gc.captureFileSnapshot(path, 20<<20)
}

func (gc *GuidedController) captureFileSnapshot(path string, limit int64) error {
	if gc == nil || path == "" {
		return nil
	}
	path = normalizeSnapshotPath(path)
	gc.mu.Lock()
	defer gc.mu.Unlock()
	if gc.journalErr != nil {
		return gc.journalErr
	}
	if _, ok := gc.snapshots[path]; ok {
		return nil
	}
	if len(gc.snapshots) >= maxSnapshotFiles {
		return fmt.Errorf("rollback snapshot file limit reached")
	}
	target, err := resolveExistingAncestorSymlinks(path, path)
	if err != nil {
		return err
	}
	snap := FileSnapshot{AbsPath: path, Target: target, Mode: 0644, Expected: "missing"}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("cannot snapshot non-regular file %s", path)
		}
		snap.Existed, snap.Mode = true, info.Mode().Perm()
		snap.Data, err = storage.ReadFile(path, limit)
		if err != nil {
			return fmt.Errorf("cannot snapshot file before mutation: %w", err)
		}
		snap.Expected = snapshotHash(snap.Data, snap.Mode)
	} else if !os.IsNotExist(err) {
		return err
	}
	total := len(snap.Data)
	for _, existing := range gc.snapshots {
		total += len(existing.Data)
	}
	if total > maxSnapshotBytes {
		return fmt.Errorf("rollback snapshots exceed 64 MiB")
	}
	if gc.snapshots == nil {
		gc.snapshots = make(map[string]FileSnapshot)
	}
	gc.snapshots[path] = snap
	return nil
}

func fileFingerprint(path string, limit int64) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("file is no longer regular: %s", path)
	}
	data, err := storage.ReadFile(path, limit)
	if err != nil {
		return "", err
	}
	return snapshotHash(data, info.Mode()), nil
}

// beginMutation records both sides of a write before the filesystem changes.
// A crash on either side of the atomic rename leaves a recognizable version.
func (gc *GuidedController) beginMutation(ctx context.Context, path string, data []byte, mode os.FileMode, check func() error) (func(), error) {
	if len(data) > maxSnapshotBytes {
		return nil, fmt.Errorf("file mutation exceeds rollback byte limit")
	}
	gc.mu.Lock()
	releaseFile, err := gc.lockRecoveryFile(ctx, path)
	if err != nil {
		gc.mu.Unlock()
		return nil, err
	}
	unlock, err := gc.lockJournal(ctx)
	if err != nil {
		releaseFile()
		gc.mu.Unlock()
		return nil, err
	}
	if check != nil {
		err = check()
	}
	if err == nil {
		err = gc.prepareMutationLocked(path, data, mode)
	}
	if err != nil {
		unlock()
		releaseFile()
		gc.mu.Unlock()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { unlock(); releaseFile(); gc.mu.Unlock() }) }, nil
}

func (gc *GuidedController) prepareMutationLocked(path string, data []byte, mode os.FileMode) error {
	snap, ok := gc.snapshots[path]
	if !ok {
		return fmt.Errorf("missing rollback snapshot for %s", path)
	}
	target, err := resolveExistingAncestorSymlinks(path, path)
	if err != nil {
		return err
	}
	if target != snap.Target {
		return fmt.Errorf("file target changed before mutation: %s", path)
	}
	current, err := fileFingerprint(path, maxSnapshotBytes)
	if err != nil {
		return err
	}
	if current != snap.Expected && current != snap.Previous {
		// The observation guard has already authorized this new edit. Rebase
		// undo on the newly observed file so later rollback preserves manual work.
		snap.Existed, snap.Data, snap.Mode = current != "missing", nil, mode
		if snap.Existed {
			snap.Data, err = storage.ReadFile(path, maxSnapshotBytes)
			if err != nil {
				return err
			}
		}
		total := len(snap.Data)
		for key, other := range gc.snapshots {
			if key != path {
				total += len(other.Data)
			}
		}
		if total > maxSnapshotBytes {
			return fmt.Errorf("rollback snapshots exceed 64 MiB")
		}
	}
	previous := gc.snapshots[path]
	next := snap
	next.Previous, next.Expected = current, snapshotHash(data, mode)
	gc.snapshots[path] = next
	if err := gc.writeJournalLocked(); err != nil {
		gc.snapshots[path] = previous
		return fmt.Errorf("save rollback recovery: %w", err)
	}
	return nil
}

func (gc *GuidedController) DiscardSnapshot(path string) {
	if gc == nil {
		return
	}
	gc.mu.Lock()
	defer gc.mu.Unlock()
	path = normalizeSnapshotPath(path)
	// Prepared writes stay recoverable even if the write reported an error.
	if snap, ok := gc.snapshots[path]; ok && snap.Previous == "" {
		delete(gc.snapshots, path)
	}
}

func (gc *GuidedController) ClearSnapshots() {
	if gc == nil {
		return
	}
	gc.mu.Lock()
	defer gc.mu.Unlock()
	gc.snapshots = make(map[string]FileSnapshot)
	gc.journalPath, gc.journalHash, gc.workspace, gc.journalErr = "", "", "", nil
}

func (gc *GuidedController) Snapshots() []FileSnapshot {
	if gc == nil {
		return nil
	}
	gc.mu.RLock()
	defer gc.mu.RUnlock()
	out := make([]FileSnapshot, 0, len(gc.snapshots))
	for _, snap := range gc.snapshots {
		snap.Data = slices.Clone(snap.Data)
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AbsPath < out[j].AbsPath })
	return out
}

func (gc *GuidedController) SnapshotCount() int {
	if gc == nil {
		return 0
	}
	gc.mu.RLock()
	defer gc.mu.RUnlock()
	return len(gc.snapshots)
}

func (gc *GuidedController) snapshotPaths() []string {
	gc.mu.RLock()
	defer gc.mu.RUnlock()
	paths := make([]string, 0, len(gc.snapshots))
	for path := range gc.snapshots {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func (gc *GuidedController) Rollback(obs *tool.FileObservationTracker) ([]string, error) {
	return gc.rollbackContext(context.Background(), obs, nil)
}

func (gc *GuidedController) rollbackContext(ctx context.Context, obs *tool.FileObservationTracker, validate func(string) (string, error)) ([]string, error) {
	if gc == nil {
		return nil, nil
	}
	gc.mu.RLock()
	loadErr := gc.journalErr
	gc.mu.RUnlock()
	if loadErr != nil {
		return nil, loadErr
	}
	var restored []string
	var failures []error
	for _, path := range gc.snapshotPaths() {
		unlock, err := filetool.LockPathContext(ctx, path)
		if err != nil {
			failures = append(failures, err)
			break
		}
		gc.mu.Lock()
		snap, ok := gc.snapshots[path]
		if ok {
			var releaseJournal func()
			releaseFile, fileErr := gc.lockRecoveryFile(ctx, snap.AbsPath)
			err = fileErr
			if err == nil {
				releaseJournal, err = gc.lockJournal(ctx)
			}
			if err == nil {
				err = gc.restoreLocked(ctx, snap, validate)
			}
			if err == nil {
				delete(gc.snapshots, snap.AbsPath)
				if saveErr := gc.writeJournalLocked(); saveErr != nil {
					gc.snapshots[snap.AbsPath] = snap
					err = saveErr
				}
				if obs != nil {
					if snap.Existed {
						obs.RecordMutation(snap.AbsPath, snap.Data)
					} else {
						obs.Forget(snap.AbsPath)
					}
				}
				restored = append(restored, snap.AbsPath)
			}
			if releaseJournal != nil {
				releaseJournal()
			}
			if releaseFile != nil {
				releaseFile()
			}
		}
		gc.mu.Unlock()
		unlock()
		if err != nil {
			failures = append(failures, fmt.Errorf("rollback %s: %w", path, err))
		}
	}
	return restored, errors.Join(failures...)
}

func (gc *GuidedController) restoreLocked(ctx context.Context, snap FileSnapshot, validate func(string) (string, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if validate != nil {
		path, err := validate(snap.AbsPath)
		if err != nil {
			return err
		}
		if path != snap.AbsPath {
			return fmt.Errorf("file target changed")
		}
	}
	target, err := resolveExistingAncestorSymlinks(snap.AbsPath, snap.AbsPath)
	if err != nil {
		return err
	}
	if target != snap.Target {
		return fmt.Errorf("file target changed")
	}
	current, err := fileFingerprint(snap.AbsPath, maxSnapshotBytes)
	if err != nil {
		return err
	}
	original := "missing"
	if snap.Existed {
		original = snapshotHash(snap.Data, snap.Mode)
	}
	if current == original {
		return nil
	}
	if current != snap.Expected && current != snap.Previous {
		return fmt.Errorf("file changed since Loop edited it; leaving it untouched")
	}
	if snap.Existed {
		return filetool.AtomicWriteFile(snap.AbsPath, snap.Data, snap.Mode)
	}
	if err := os.Remove(snap.AbsPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return storage.SyncDirectory(filepath.Dir(snap.AbsPath))
}
