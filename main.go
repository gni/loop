package main

import (
	"context"
	"fmt"
	"os"
	"sync"

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

	var (
		exitMu   sync.Mutex
		exitCode int
	)

	// Registered first so it runs last, after the terminal cleanup defers. The default
	// lifecycle exit function calls os.Exit inside the signal path, which skips every
	// defer in main and leaves the alt screen and cursor dirty on Ctrl+C.
	defer func() {
		// Re-panic so a real panic still prints its traceback; os.Exit here would
		// swallow it.
		if r := recover(); r != nil {
			panic(r)
		}
		exitMu.Lock()
		code := exitCode
		exitMu.Unlock()
		if code == 0 && supervisor.Context().Err() != nil {
			code = supervisor.ExitCode()
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
	supervisor.SetExitFunc(func(code int) {
		exitMu.Lock()
		exitCode = code
		exitMu.Unlock()
	})
	supervisor.SetForceExitFunc(func(code int) {
		// A blocked hook may hold a terminal mutex. Restore the original
		// terminal directly rather than acquiring application locks again.
		if originalTerminal != nil {
			_ = termpkg.Restore(originalTerminalFD, originalTerminal)
			output := os.Stderr
			if !termpkg.IsTerminal(int(output.Fd())) {
				output = os.Stdout
			}
			if termpkg.IsTerminal(int(output.Fd())) {
				_, _ = output.WriteString(terminal.ExitAlternateScreen + terminal.DisableBracketedPaste + "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[r\x1b[0m")
			}
		}
		os.Exit(code)
	})

	supervisor.Start()
	defer supervisor.Stop()
	defer supervisor.Wait(context.Background())

	if err := cmd.ExecuteContext(supervisor.Context()); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		exitMu.Lock()
		if exitCode == 0 {
			exitCode = 1
		}
		exitMu.Unlock()
	}
}
