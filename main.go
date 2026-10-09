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

	defer term.RecoverPanic(func() {
		term.ForceExitAltScreen()
	})
	defer term.ForceExitAltScreen()

	supervisor := lifecycle.Default()
	supervisor.OnShutdown(func() {
		term.ForceExitAltScreen()
	})

	supervisor.Start()
	defer supervisor.Stop()

	if err := cmd.ExecuteContext(supervisor.Context()); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
