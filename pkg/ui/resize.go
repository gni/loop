package ui

import (
	"io"
	"reflect"
	"unsafe"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"loop/pkg/agent"
)

// SetNonCanonical places terminal in raw non-canonical mode during turn execution.
func SetNonCanonical(fd int) (func(), error) {
	return setNonCanonical(fd)
}

func setNonCanonical(fd int) (func(), error) {
	if !term.IsTerminal(fd) {
		return func() {}, nil
	}
	termios, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}
	oldTermios := *termios

	termios.Lflag &^= unix.ICANON | unix.ECHO
	termios.Cc[unix.VMIN] = 1
	termios.Cc[unix.VTIME] = 0

	err = unix.IoctlSetTermios(fd, unix.TCSETS, termios)
	if err != nil {
		return nil, err
	}

	restore := func() {
		_ = unix.IoctlSetTermios(fd, unix.TCSETS, &oldTermios)
	}
	return restore, nil
}

// HandleResize updates terminal sizes and redraws controls on SIGWINCH.
func HandleResize(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal) {
	handleResize(w, a, kiReader, rl)
}

func handleResize(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()

	width, height := GetTerminalSize()
	if height <= 3 {
		return
	}

	if rl != nil {
		rl.SetSize(width, height)
	}

	getUI().StateMu.Lock()
	activeOp := getUI().ActiveCancelFunc != nil || getUI().State.IsGenerating
	getUI().StateMu.Unlock()

	cw := crnlWriter{W: w}
	drawConsoleStaticControlsLocked(cw, a, kiReader, rl, !activeOp)

	if !activeOp && a.CurrentWriter != nil {
		if pw, ok := a.CurrentWriter.(*PromptPreservingWriter); ok {
			pw.ForceRepositionLocked()
		} else if fr, ok := a.CurrentWriter.(interface{ ForceReposition() }); ok {
			fr.ForceReposition()
		}
	}
}

func getTerminalLine(rl *term.Terminal) (string, int) {
	if rl == nil {
		return "", 0
	}
	val := reflect.ValueOf(rl).Elem()
	lineField := val.FieldByName("line")
	posField := val.FieldByName("pos")
	if lineField.IsValid() && posField.IsValid() {
		ptrLine := unsafe.Pointer(lineField.UnsafeAddr())
		runes := *(*[]rune)(ptrLine)

		ptrPos := unsafe.Pointer(posField.UnsafeAddr())
		pos := *(*int)(ptrPos)

		return string(runes), pos
	}
	return "", 0
}
