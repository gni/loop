package storage

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const MaxSpillBytes = 32 << 20
const spillPrefix = "loop-output-"

func NewSpillPath(session, prefix string) string {
	return filepath.ToSlash(filepath.Join(".loop", "spill", session, spillPrefix+prefix+"-"+rand.Text()+".log"))
}

func splitSpillPath(path string) (session, name string, ok bool) {
	if !filepath.IsLocal(path) {
		return "", "", false
	}
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	if len(parts) != 4 || parts[0] != ".loop" || parts[1] != "spill" || !strings.HasSuffix(parts[3], ".log") {
		return "", "", false
	}
	return parts[2], parts[3], true
}

func IsSpillPath(path string) bool {
	_, _, ok := splitSpillPath(path)
	return ok
}

func ownedSpill(name string) bool {
	return strings.HasPrefix(name, spillPrefix) && strings.HasSuffix(name, ".log")
}

// Open each directory relative to its already opened parent. Root prevents
// escapes even if a path component is replaced during the operation.
func openSpillDirectory(ctx context.Context, workspace, session string, create bool) (*os.Root, error) {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{".loop", "spill", session} {
		if err := ctx.Err(); err != nil {
			_ = root.Close()
			return nil, err
		}
		if create {
			if err := root.Mkdir(name, 0700); err != nil && !os.IsExist(err) {
				_ = root.Close()
				return nil, err
			}
		}
		info, err := root.Lstat(name)
		if err != nil || !info.IsDir() {
			_ = root.Close()
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("spill directory %q must be a real directory", name)
		}
		child, err := root.OpenRoot(name)
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		opened, err := child.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			_ = child.Close()
			return nil, fmt.Errorf("spill directory %q changed while opening", name)
		}
		root = child
	}
	return root, nil
}

func OpenSpill(ctx context.Context, workspace, path string) (*os.File, error) {
	session, name, ok := splitSpillPath(path)
	if !ok {
		return nil, fmt.Errorf("invalid spill path")
	}
	dir, err := openSpillDirectory(ctx, workspace, session, false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return openRegularFile(name, dir.OpenFile)
}

func WriteSpill(ctx context.Context, workspace, path, data string, maxFiles int, maxAge time.Duration) (err error) {
	session, name, ok := splitSpillPath(path)
	if !ok || !ownedSpill(name) {
		return fmt.Errorf("invalid spill path")
	}
	if len(data) > MaxSpillBytes {
		return fmt.Errorf("spill exceeds %d MiB", MaxSpillBytes>>20)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := openSpillDirectory(ctx, workspace, session, true)
	if err != nil {
		return err
	}
	defer dir.Close()
	file, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = file.Close()
			_ = dir.Remove(name)
		}
	}()
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := file.WriteString(data[:min(len(data), 64<<10)])
		if err != nil {
			return err
		}
		data = data[n:]
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	// Retention is best effort after a complete file has been saved.
	_ = pruneSpills(ctx, dir, maxFiles, maxAge, name)
	return nil
}

func PruneSpillDirectory(path string, maxFiles int, maxAge time.Duration) error {
	if path == "" {
		return nil
	}
	dir, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return pruneSpills(context.Background(), dir, maxFiles, maxAge, "")
}

func pruneSpills(ctx context.Context, root *os.Root, maxFiles int, maxAge time.Duration, keep string) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	type entry struct {
		name string
		date time.Time
	}
	var remaining []entry
	var failures []error
	now := time.Now()
	remove := func(name string) bool {
		if err := root.Remove(name); err != nil && !os.IsNotExist(err) {
			failures = append(failures, err)
			return false
		}
		return true
	}
	for _, item := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !ownedSpill(item.Name()) || !item.Type().IsRegular() {
			continue
		}
		info, err := item.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if item.Name() != keep && maxAge > 0 && now.Sub(info.ModTime()) > maxAge && remove(item.Name()) {
			continue
		}
		remaining = append(remaining, entry{name: item.Name(), date: info.ModTime()})
	}
	if maxFiles > 0 && len(remaining) > maxFiles {
		sort.Slice(remaining, func(i, j int) bool {
			if remaining[i].date.Equal(remaining[j].date) {
				return remaining[i].name < remaining[j].name
			}
			return remaining[i].date.Before(remaining[j].date)
		})
		removeCount := len(remaining) - maxFiles
		for _, item := range remaining {
			if err := ctx.Err(); err != nil {
				return err
			}
			if removeCount == 0 {
				break
			}
			if item.name != keep && remove(item.name) {
				removeCount--
			}
		}
	}
	return errors.Join(failures...)
}
