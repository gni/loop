package main

import (
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
	if termpkg.IsTerminal(int(os.Stderr.Fd())) {
		term.RestoreCursor()
	}

	var (
		exitMu    sync.Mutex
		exitCode  int
		shutdowns int
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
		if code != 0 {
			os.Exit(code)
		}
	}()

	defer term.RecoverPanic(func() {
		term.ForceExitAltScreen()
	})
	defer term.ForceExitAltScreen()

	supervisor := lifecycle.Default()
	supervisor.OnShutdown(func() {
		term.ForceExitAltScreen()
	})

	// The first shutdown only records the code so main's deferred cleanup can run. A
	// repeated shutdown (double interrupt) still terminates the process immediately.
	supervisor.SetExitFunc(func(code int) {
		exitMu.Lock()
		shutdowns++
		forceNow := shutdowns > 1
		exitCode = code
		exitMu.Unlock()
		if forceNow {
			os.Exit(code)
		}
	})

	supervisor.Start()
	defer supervisor.Stop()

	if err := cmd.ExecuteContext(supervisor.Context()); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		exitMu.Lock()
		if exitCode == 0 {
			exitCode = 1
		}
		exitMu.Unlock()
	}
}
