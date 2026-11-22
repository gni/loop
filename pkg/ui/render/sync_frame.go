package render

import "io"

type syncFrameMarker interface {
	inSyncFrame() bool
}

// syncFrameWriter marks an io.Writer as already inside a DEC Mode 2026
// synchronized output frame so nested footer/status render helpers do not emit
// redundant begin/end sync sequences. It intentionally does not implement
// Unwrap() io.Writer so helpers like agent.UnwrapWriter preserve the marker.
type syncFrameWriter struct {
	w io.Writer
}

func (s syncFrameWriter) Write(p []byte) (int, error) {
	return s.w.Write(p)
}

func (s syncFrameWriter) WriteString(str string) (int, error) {
	return io.WriteString(s.w, str)
}

func (s syncFrameWriter) inSyncFrame() bool {
	return true
}

// Height forwards terminal height queries to wrapped writers when supported.
func (s syncFrameWriter) Height() int {
	curr := s.w
	for depth := 0; curr != nil && depth < 16; depth++ {
		if hg, ok := curr.(interface{ Height() int }); ok {
			return hg.Height()
		}
		u, ok := curr.(interface{ Unwrap() io.Writer })
		if !ok {
			break
		}
		next := u.Unwrap()
		if next == nil || next == curr {
			break
		}
		curr = next
	}
	return 0
}

// WrapSyncFrame returns a writer that suppresses nested Mode 2026 sync markers.
func WrapSyncFrame(w io.Writer) io.Writer {
	if w == nil || inSyncFrame(w) {
		return w
	}
	return syncFrameWriter{w: w}
}

func inSyncFrame(w io.Writer) bool {
	for depth := 0; w != nil && depth < 16; depth++ {
		if m, ok := w.(syncFrameMarker); ok && m.inSyncFrame() {
			return true
		}
		u, ok := w.(interface{ Unwrap() io.Writer })
		if !ok {
			return false
		}
		next := u.Unwrap()
		if next == nil || next == w {
			return false
		}
		w = next
	}
	return false
}
