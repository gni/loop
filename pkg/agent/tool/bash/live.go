package bash

import (
	"bytes"
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
		end := min(len(p), written+4096)
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

// markerFilter removes the private pwd boundary while allowing partial lines
// to reach the terminal. It holds at most the possible marker prefix.
type markerFilter struct {
	w        io.Writer
	needle   []byte
	pending  []byte
	finished bool
}

func newMarkerFilter(w io.Writer, marker string) *markerFilter {
	return &markerFilter{w: w, needle: []byte("\n" + marker)}
}

func (f *markerFilter) Write(p []byte) (int, error) {
	length := len(p)
	if f.finished || length == 0 {
		return length, nil
	}
	f.pending = append(f.pending, p...)
	if idx := bytes.Index(f.pending, f.needle); idx >= 0 {
		if _, err := f.w.Write(f.pending[:idx]); err != nil {
			return 0, err
		}
		f.pending = nil
		f.finished = true
		return length, nil
	}
	hold := 0
	for n := min(len(f.pending), len(f.needle)-1); n > 0; n-- {
		if bytes.Equal(f.pending[len(f.pending)-n:], f.needle[:n]) {
			hold = n
			break
		}
	}
	if _, err := f.w.Write(f.pending[:len(f.pending)-hold]); err != nil {
		return 0, err
	}
	f.pending = append(f.pending[:0], f.pending[len(f.pending)-hold:]...)
	return length, nil
}

func (f *markerFilter) Finish() {
	if f.finished || len(f.pending) == 0 {
		return
	}
	_, _ = f.w.Write(f.pending)
	f.pending = nil
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
	nextSample time.Time
}

func (l *limitedLiveWriter) Write(p []byte) (int, error) {
	original := len(p)
	if l.remaining > 0 {
		show := min(original, l.remaining)
		if _, err := l.w.Write(p[:show]); err != nil {
			return 0, err
		}
		l.remaining -= show
		p = p[show:]
	}
	if len(p) == 0 {
		return original, nil
	}
	if !l.notified {
		l.notified = true
		_, _ = fmt.Fprintf(l.w, "\n[live output sampled after %d bytes; showing recent output]\n", l.limit)
	}
	const sampleBytes = 4096
	if len(p) >= sampleBytes {
		l.pending = append(l.pending[:0], p[len(p)-sampleBytes:]...)
	} else {
		l.pending = append(l.pending, p...)
		if len(l.pending) > sampleBytes {
			l.pending = append(l.pending[:0], l.pending[len(l.pending)-sampleBytes:]...)
		}
	}
	if time.Now().After(l.nextSample) {
		l.flushSample()
		l.nextSample = time.Now().Add(250 * time.Millisecond)
	}
	return original, nil
}

func (l *limitedLiveWriter) flushSample() {
	data := l.pending
	for len(data) > 0 && !utf8.RuneStart(data[0]) {
		data = data[1:]
	}
	if len(data) > 0 {
		_, _ = l.w.Write(data)
	}
	l.pending = l.pending[:0]
}

// bestEffortLiveWriter must not stop draining a child process if terminal output fails.
type bestEffortLiveWriter struct{ io.Writer }

func (w bestEffortLiveWriter) Write(p []byte) (int, error) {
	_, _ = w.Writer.Write(p)
	return len(p), nil
}

func (s *liveShellOutput) Streams(marker string) (io.Writer, io.Writer, func()) {
	liveLimit := s.limit
	outLimit := &limitedLiveWriter{w: s, remaining: liveLimit, limit: liveLimit}
	errLimit := &limitedLiveWriter{w: s, remaining: liveLimit, limit: liveLimit}
	stdout := newMarkerFilter(output.NewSafeShellOutputWriter(outLimit), marker)
	stderr := output.NewSafeShellOutputWriter(errLimit)
	return stdout, stderr, func() { stdout.Finish(); outLimit.flushSample(); errLimit.flushSample() }
}
