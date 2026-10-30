package bash

import (
	"bytes"
	"io"
	"sync"

	"loop/pkg/domain/output"
	domaintool "loop/pkg/domain/tool"
)

// liveShellOutput serializes stdout and stderr before they reach the terminal.
// Captured tool output remains separate and authoritative for model history.
type liveShellOutput struct {
	mu       sync.Mutex
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
	return &liveShellOutput{writer: w, context: stream}
}

func (s *liveShellOutput) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.writer.Write(p)
	if n > 0 {
		s.written = true
		s.lastByte = p[n-1]
		s.context.SetLiveBodyStreamed(true)
	}
	return n, err
}

func (s *liveShellOutput) Finish() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.written {
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

// limitedLiveWriter bounds terminal output independently of the captured result.
// After reaching the limit, pipe reads continue so the child cannot block.
type limitedLiveWriter struct {
	w         io.Writer
	remaining int
	notified  bool
}

func (l *limitedLiveWriter) Write(p []byte) (int, error) {
	original := len(p)
	if l.remaining <= 0 {
		if original > 0 {
			l.notice()
		}
		return original, nil
	}
	show := min(original, l.remaining)
	if _, err := l.w.Write(p[:show]); err != nil {
		return 0, err
	}
	l.remaining -= show
	if show < original {
		l.notice()
	}
	return original, nil
}

func (l *limitedLiveWriter) notice() {
	if l.notified {
		return
	}
	l.notified = true
	_, _ = io.WriteString(l.w, "\n[live output truncated after 8 MiB]\n")
}

// bestEffortLiveWriter must not stop draining a child process if terminal output fails.
type bestEffortLiveWriter struct{ io.Writer }

func (w bestEffortLiveWriter) Write(p []byte) (int, error) {
	_, _ = w.Writer.Write(p)
	return len(p), nil
}

func (s *liveShellOutput) Streams(marker string) (io.Writer, io.Writer, func()) {
	const liveLimit = 8 << 20
	stdout := newMarkerFilter(output.NewSafeShellOutputWriter(&limitedLiveWriter{w: s, remaining: liveLimit}), marker)
	stderr := output.NewSafeShellOutputWriter(&limitedLiveWriter{w: s, remaining: liveLimit})
	return stdout, stderr, stdout.Finish
}
