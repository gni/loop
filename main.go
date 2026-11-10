package main

import (
	"context"
	"fmt"
	"os"
	"time"

	termpkg "golang.org/x/term"
	"loop/pkg/cmd"
	"loop/pkg/lifecycle"
	"loop/pkg/terminal"
)

func main() {
	term := terminal.Default()
	supervisor := lifecycle.Default()
	if termpkg.IsTerminal(int(os.Stderr.Fd())) {
		term.RestoreCursor()
	}

	var originalTerminal *termpkg.State
	var originalTerminalFD int
	for _, file := range []*os.File{os.Stdin, os.Stderr, os.Stdout} {
		if termpkg.IsTerminal(int(file.Fd())) {
			originalTerminalFD = int(file.Fd())
			originalTerminal, _ = termpkg.GetState(originalTerminalFD)
			break
		}
	}

	exitCode := 0

	// Registered first so it runs last, after the terminal cleanup defers. The default
	// lifecycle exit function calls os.Exit inside the signal path, which skips every
	// defer in main and leaves the alt screen and cursor dirty on Ctrl+C.
	defer func() {
		// Re-panic so a real panic still prints its traceback; os.Exit here would
		// swallow it.
		if r := recover(); r != nil {
			panic(r)
		}
		code := exitCode
		if supervisor.Context().Err() != nil {
			if requested := supervisor.ExitCode(); requested != 0 || code == 0 {
				code = requested
			}
		}
		if code != 0 {
			os.Exit(code)
		}
	}()

	defer term.RecoverPanic(func() {
		term.ForceExitAltScreen()
	})
	defer term.ForceExitAltScreen()

	supervisor.OnShutdown(func() {
		term.ForceExitAltScreen()
	})

	// Normal shutdown leaves main's defers in control of cleanup.
	supervisor.SetExitFunc(func(int) {})
	supervisor.SetForceExitFunc(func(code int) {
		restoreTerminalForExit(originalTerminalFD, originalTerminal)
		os.Exit(code)
	})

	supervisor.Start()
	defer supervisor.Stop()
	defer supervisor.Wait(context.Background())

	if err := cmd.ExecuteContext(supervisor.Context()); err != nil {
		if supervisor.Context().Err() != nil && isShutdownCancellation(err) {
			return
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		exitCode = 1
	}
}

func restoreTerminalForExit(fd int, state *termpkg.State) {
	if state == nil {
		return
	}
	restored := make(chan struct{})
	go func() {
		defer close(restored)
		_ = termpkg.Restore(fd, state)
		output := os.Stderr
		if !termpkg.IsTerminal(int(output.Fd())) {
			output = os.Stdout
		}
		if termpkg.IsTerminal(int(output.Fd())) {
			_, _ = output.WriteString(terminal.ExitAlternateScreen + terminal.DisableBracketedPaste + "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[r\x1b[0m")
		}
	}()
	// Even a direct terminal write may block on a full buffer or file lock.
	// Forced exit gives restoration a brief chance, then leaves cleanup behind.
	timer := time.NewTimer(150 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-restored:
	case <-timer.C:
	}
}

// A requested shutdown is not a command failure. Joined cleanup failures must
// still be reported, even when one of their causes is context cancellation.
func isShutdownCancellation(err error) bool {
	if err == context.Canceled {
		return true
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		causes := wrapped.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !isShutdownCancellation(cause) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return isShutdownCancellation(wrapped.Unwrap())
	}
	return false
}
