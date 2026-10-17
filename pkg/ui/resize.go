package ui

import (
	"io"
	"reflect"
	"unsafe"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"loop/pkg/agent"
)

func SetNonCanonical(fd int) (func(), error) {
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

	if err := unix.IoctlSetTermios(fd, unix.TCSETS, termios); err != nil {
		return nil, err
	}

	return func() {
		_ = unix.IoctlSetTermios(fd, unix.TCSETS, &oldTermios)
	}, nil
}

func HandleResize(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal) {
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
	inModal := getUI().InApprovalPrompt
	getUI().StateMu.Unlock()
	if inModal {
		return
	}

	cw := crnlWriter{W: w}
	DrawConsoleStaticControlsLocked(cw, a, kiReader, rl, !activeOp)

	if !activeOp && a.CurrentWriter() != nil {
		if pw, ok := a.CurrentWriter().(*PromptPreservingWriter); ok {
			pw.ForceRepositionLocked()
		} else if fr, ok := a.CurrentWriter().(interface{ ForceReposition() }); ok {
			fr.ForceReposition()
		}
	}
}

func getTerminalLine(rl *term.Terminal) (line string, pos int) {
	if rl == nil {
		return "", 0
	}
	defer func() {
		if recover() != nil {
			line = ""
			pos = 0
		}
	}()

	val := reflect.ValueOf(rl)
	if val.Kind() != reflect.Ptr || val.IsNil() {
		return "", 0
	}
	elem := val.Elem()
	if elem.Kind() != reflect.Struct {
		return "", 0
	}
	lineField := elem.FieldByName("line")
	posField := elem.FieldByName("pos")
	if lineField.IsValid() && lineField.CanAddr() && lineField.Kind() == reflect.Slice &&
		posField.IsValid() && posField.CanAddr() && posField.Kind() == reflect.Int {
		runes := *(*[]rune)(unsafe.Pointer(lineField.UnsafeAddr()))
		posVal := *(*int)(unsafe.Pointer(posField.UnsafeAddr()))
		return string(runes), posVal
	}
	return "", 0
}
