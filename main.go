package main

import (
	"fmt"
	"os"

	"loop/pkg/cmd"
	"loop/pkg/lifecycle"
	"loop/pkg/terminal"
)

func main() {
	term := terminal.Default()
	term.RestoreCursor()

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
		if exitCode != 0 {
			os.Exit(exitCode)
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
	shutdowns := 0
	supervisor.SetExitFunc(func(code int) {
		shutdowns++
		if shutdowns > 1 {
			os.Exit(code)
		}
		exitCode = code
	})

	supervisor.Start()
	defer supervisor.Stop()

	if err := cmd.ExecuteContext(supervisor.Context()); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		if exitCode == 0 {
			exitCode = 1
		}
	}
}
