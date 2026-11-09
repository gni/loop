package db

import (
	"context"
	"io"
	"path/filepath"
	"time"

	"loop/pkg/storage"
)

// File locks precede the cache mutex. Waiting for another process must never
// reserve the cache for that entire wait. Cancellation is checked again before
// returning ownership; a started mutation finishes its commit or rollback.
func (s *JSONLStore) lockStore(ctx context.Context, exclusive bool) (func(), error) {
	releaseFile, err := s.fileLock(ctx, exclusive)
	if err != nil {
		return nil, err
	}
	if ctx.Done() == nil {
		s.core.mu.Lock()
	} else {
		for !s.core.mu.TryLock() {
			timer := time.NewTimer(20 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				releaseFile()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	if err := ctx.Err(); err != nil {
		s.core.mu.Unlock()
		releaseFile()
		return nil, err
	}
	return func() {
		s.core.mu.Unlock()
		releaseFile()
	}, nil
}

func (s *JSONLStore) fileLock(ctx context.Context, exclusive bool) (func(), error) {
	return storage.LockContext(ctx, filepath.Join(s.core.dir, ".store.lock"), exclusive)
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.Reader.Read(p)
	if cancelled := r.ctx.Err(); cancelled != nil {
		return 0, cancelled
	}
	return n, err
}

func readSessionFile(ctx context.Context, path string) ([]byte, error) {
	file, err := storage.OpenRegularFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(contextReader{ctx, file})
}
