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

// LockMutexContext acquires mu while honoring context cancellation without
// leaving a goroutine blocked on mu.Lock after cancellation.
func LockMutexContext(ctx context.Context, mu *sync.Mutex) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ctx.Done() == nil {
		mu.Lock()
		return nil
	}
	if mu.TryLock() {
		if err := ctx.Err(); err != nil {
			mu.Unlock()
			return err
		}
		return nil
	}
	delay := time.Millisecond
	const maxDelay = 20 * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		if mu.TryLock() {
			if err := ctx.Err(); err != nil {
				mu.Unlock()
				return err
			}
			return nil
		}
		delay = min(delay*2, maxDelay)
		timer.Reset(delay)
	}
}

func waitFileLock(ctx context.Context, file *os.File, exclusive bool) error {
	if ctx.Done() == nil {
		return flock(file, exclusive)
	}
	delay := time.Millisecond
	const maxDelay = 20 * time.Millisecond
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
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
		if timer == nil {
			timer = time.NewTimer(delay)
		} else {
			timer.Reset(delay)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, maxDelay)
	}
}
