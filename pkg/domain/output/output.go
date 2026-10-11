// Package output holds the single-writer output guard shared by the agent side and
// the UI side. One writer owns the terminal; a stream or extension can take it over
// exclusively, and Flush drains buffered bytes before ownership changes.
package output

import (
	"bytes"
	"io"
	"sync"
)

// OutputGuard is the single-writer output guard. It has one ownership state (taken)
// and one buffering mode:
//   - buffered mode accumulates writes and emits them on Flush, so a stream can take
//     ownership cleanly before anything reaches the terminal;
//   - passthrough mode writes straight to the owned writer, and during takeover routes
//     writes to stderr so they cannot interleave with the guarded stream.
//
// Both modes preserve the behaviour of the two guards this type replaces.
type OutputGuard struct {
	mu       sync.Mutex
	inner    io.Writer
	stderr   io.Writer
	taken    bool
	buffered bool
	buf      bytes.Buffer
}

// New returns a buffered guard owning w.
func New(w io.Writer) *OutputGuard { return &OutputGuard{inner: w, buffered: true} }

// NewPassthrough returns a guard that writes through to w and diverts to stderr while
// taken over.
func NewPassthrough(w, stderr io.Writer) *OutputGuard {
	return &OutputGuard{inner: w, stderr: stderr}
}

// TakeOver grants exclusive ownership of the terminal to the caller. It reports false
// when the guard is already taken over.
func (g *OutputGuard) TakeOver() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.taken {
		return false
	}
	g.taken = true
	return true
}

// Restore releases terminal ownership back to the default writer.
func (g *OutputGuard) Restore() {
	g.mu.Lock()
	g.taken = false
	g.mu.Unlock()
}

// IsTakenOver reports whether the guard has been taken over.
func (g *OutputGuard) IsTakenOver() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.taken
}

// Write is the serialized single-writer path. During takeover, writes are diverted to
// stderr when one is configured; otherwise a buffered guard accumulates and a
// passthrough guard writes through.
func (g *OutputGuard) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.taken && g.stderr != nil {
		return g.stderr.Write(p)
	}
	if g.buffered {
		g.buf.Write(p)
		return len(p), nil
	}
	return g.inner.Write(p)
}

// Flush drains buffered output in one atomic write. For a passthrough guard there is
// nothing buffered, so it is a no-op.
func (g *OutputGuard) Flush() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.buf.Len() == 0 {
		return nil
	}
	_, err := g.inner.Write(g.buf.Bytes())
	g.buf.Reset()
	return err
}
