package storage

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"
)

func Lock(path string, exclusive bool) (func(), error) {
	return LockContext(context.Background(), path, exclusive)
}

// LockContext waits without leaving a goroutine or descriptor blocked in the
// kernel after cancellation. Background callers retain the blocking fast path.
func LockContext(ctx context.Context, path string, exclusive bool) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := waitFileLock(ctx, file, exclusive); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = funlock(file)
		_ = file.Close()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { _ = funlock(file); _ = file.Close() }) }, nil
}

func waitFileLock(ctx context.Context, file *os.File, exclusive bool) error {
	if ctx.Done() == nil {
		return flock(file, exclusive)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		locked, err := tryFlock(file, exclusive)
		if err != nil {
			return err
		}
		if locked {
			return nil
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
