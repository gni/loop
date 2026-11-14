package bash

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
	"unicode/utf8"

	"loop/pkg/domain/limits"
	"loop/pkg/domain/output"
	domaintool "loop/pkg/domain/tool"
)

// liveShellOutput serializes stdout and stderr before they reach the terminal.
// Captured tool output remains separate and authoritative for model history.
type liveShellOutput struct {
	mu       sync.Mutex
	ctx      context.Context
	limit    int
	writer   io.Writer
	context  domaintool.LiveOutputWriter
	lastByte byte
	written  bool
}

func newLiveShellOutput(ctx domaintool.AgentContext) *liveShellOutput {
	stream, ok := ctx.(domaintool.LiveOutputWriter)
	if !ok {
		return nil
	}
	w := stream.GetLiveWriter()
	if w == nil {
		return nil
	}
	return &liveShellOutput{writer: w, context: stream, ctx: ctx.Context(), limit: limits.For(ctx).MaxOutputBytes}
}

func (s *liveShellOutput) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return len(p), nil
	}
	// Keep terminal writes small so cancellation is observed between chunks.
	written := 0
	for written < len(p) {
		if s.ctx.Err() != nil {
			return len(p), nil
		}
		end := written + liveTextPrefix(p[written:], 4096)
		n, err := s.writer.Write(p[written:end])
		if n > 0 {
			s.written = true
			s.lastByte = p[written+n-1]
			s.context.SetLiveBodyStreamed(true)
		}
		written += n
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func (s *liveShellOutput) Finish() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.written && s.ctx.Err() == nil {
		_, _ = io.WriteString(s.writer, "\x1b[0m")
		if s.lastByte != '\n' {
			_, _ = io.WriteString(s.writer, "\n")
		}
	}
}

// limitedLiveWriter shows the initial budget in full, then samples recent
// output at a bounded rate. Long commands remain visible without flooding the
// terminal or retaining an unbounded queue of pending output.
type limitedLiveWriter struct {
	w          io.Writer
	remaining  int
	notified   bool
	limit      int
	pending    []byte
	utf8Tail   []byte
	nextSample time.Time
}

func (l *limitedLiveWriter) Write(p []byte) (int, error) {
	original := len(p)
	if original == 0 {
		return 0, nil
	}
	if len(l.utf8Tail) > 0 {
		for len(p) > 0 && !utf8.FullRune(l.utf8Tail) && !utf8.RuneStart(p[0]) {
			l.utf8Tail = append(l.utf8Tail, p[0])
			p = p[1:]
		}
		if !utf8.FullRune(l.utf8Tail) && len(p) == 0 {
			return original, nil
		}
		tail := l.utf8Tail
		if !utf8.Valid(tail) {
			tail = []byte("\uFFFD")
		}
		if err := l.writeComplete(tail); err != nil {
			return 0, err
		}
		l.utf8Tail = l.utf8Tail[:0]
	}
	if end := liveTextEnd(p); end < len(p) {
		l.utf8Tail = append(l.utf8Tail[:0], p[end:]...)
		p = p[:end]
	}
	if err := l.writeComplete(p); err != nil {
		return 0, err
	}
	return original, nil
}

func (l *limitedLiveWriter) writeComplete(p []byte) error {
	if l.remaining > 0 {
		show := liveTextPrefix(p, l.remaining)
		if show > 0 {
			if err := l.write(p[:show]); err != nil {
				return err
			}
		}
		l.remaining -= show
		p = p[show:]
		if len(p) > 0 {
			l.remaining = 0
		}
	}
	if len(p) == 0 {
		return nil
	}
	if !l.notified {
		notice := fmt.Sprintf("\x1b[0m\n[live output sampled after %d bytes; showing recent output]\n", l.limit)
		if err := l.write([]byte(notice)); err != nil {
			return err
		}
		l.notified = true
	}
	const sampleBytes = 4096
	if len(p) >= sampleBytes {
		l.pending = append(l.pending[:0], liveTextSuffix(p, sampleBytes)...)
	} else {
		keep := liveTextSuffix(l.pending, sampleBytes-len(p))
		copy(l.pending, keep)
		l.pending = l.pending[:len(keep)]
		l.pending = append(l.pending, p...)
	}
	if time.Now().After(l.nextSample) {
		if err := l.flushSample(); err != nil {
			return err
		}
		l.nextSample = time.Now().Add(250 * time.Millisecond)
	}
	return nil
}

func (l *limitedLiveWriter) write(p []byte) error {
	n, err := l.w.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return err
}

func (l *limitedLiveWriter) flushSample() error {
	if len(l.pending) == 0 {
		return nil
	}
	err := l.write(l.pending)
	l.pending = l.pending[:0]
	return err
}

func (l *limitedLiveWriter) finish() error {
	if len(l.utf8Tail) > 0 {
		l.utf8Tail = l.utf8Tail[:0]
		if err := l.writeComplete([]byte("\uFFFD")); err != nil {
			return err
		}
	}
	return l.flushSample()
}

func (s *liveShellOutput) Streams() (io.Writer, io.Writer, func()) {
	liveLimit := s.limit
	outLimit := &limitedLiveWriter{w: s, remaining: liveLimit, limit: liveLimit}
	errLimit := &limitedLiveWriter{w: s, remaining: liveLimit, limit: liveLimit}
	stdout := output.NewSafeShellOutputWriter(outLimit)
	stderr := output.NewSafeShellOutputWriter(errLimit)
	return stdout, stderr, func() { _ = outLimit.finish(); _ = errLimit.finish() }
}
