package agent

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sync"
)

const toolProgressMemoryBytes = 256 << 10

// toolProgressBuffer keeps small outputs in memory and larger replays in an
// append-only temporary file. A snapshot sees only its committed prefix.
type toolProgressBuffer struct {
	mu                         sync.Mutex
	memory                     bytes.Buffer
	file                       *os.File
	diskBytes, size            int64
	newlines, trailingNewlines int
	readers                    int
	closed                     bool
	err                        error
}

type toolProgressSnapshot struct {
	io.Reader
	size    int64
	lines   int
	err     error
	release func()
}

func (s *toolProgressSnapshot) Close() error {
	if s.release != nil {
		s.release()
	}
	return nil
}

func (b *toolProgressBuffer) append(p []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || len(p) == 0 {
		return
	}
	b.size += int64(len(p))
	for _, c := range p {
		if c == '\n' {
			b.newlines++
			b.trailingNewlines++
		} else if c != '\r' {
			b.trailingNewlines = 0
		}
	}
	if b.file == nil && b.err == nil && b.memory.Len()+len(p) > toolProgressMemoryBytes {
		f, err := os.CreateTemp("", "loop-live-output-*")
		if err != nil {
			b.err = fmt.Errorf("create live output cache: %w", err)
		} else {
			b.file = f
			_ = os.Remove(f.Name())
			n, err := f.Write(b.memory.Bytes())
			b.diskBytes = int64(n)
			b.memory.Next(n)
			if err == nil && b.memory.Len() > 0 {
				err = io.ErrShortWrite
			}
			if err != nil {
				b.err = fmt.Errorf("write live output cache: %w", err)
			} else {
				b.memory = bytes.Buffer{}
			}
		}
	}
	if b.file != nil && b.err == nil {
		n, err := b.file.Write(p)
		b.diskBytes += int64(n)
		if err == nil && n == len(p) {
			return
		}
		if err == nil {
			err = io.ErrShortWrite
		}
		b.err = fmt.Errorf("write live output cache: %w", err)
		p = p[n:]
	}
	// Cache failures must not fail a command or discard presented output.
	// Retain the unwritten tail in memory and expose the cache error on replay.
	_, _ = b.memory.Write(p)
}

func (b *toolProgressBuffer) snapshot() toolProgressSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := toolProgressSnapshot{Reader: bytes.NewReader(nil)}
	if b.closed {
		return s
	}
	s.size, s.lines, s.err = b.size, b.newlines-b.trailingNewlines+1, b.err
	s.Reader = bytes.NewReader(bytes.Clone(b.memory.Bytes()))
	if b.file != nil {
		b.readers++
		s.Reader = io.MultiReader(io.NewSectionReader(b.file, 0, b.diskBytes), s.Reader)
		var once sync.Once
		s.release = func() {
			once.Do(func() {
				b.mu.Lock()
				defer b.mu.Unlock()
				b.readers--
				if b.closed && b.readers == 0 {
					b.closeFileLocked()
				}
			})
		}
	}
	return s
}

func (b *toolProgressBuffer) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.memory = bytes.Buffer{}
	if b.readers == 0 {
		b.closeFileLocked()
	}
}

func (b *toolProgressBuffer) closeFileLocked() {
	if b.file != nil {
		_ = b.file.Close()
		_ = os.Remove(b.file.Name())
		b.file = nil
	}
}
