package commands

import (
	"fmt"
	"io"
	"os"

	"loop/pkg/agent"
)

func handleDebugCommand(a *agent.Agent, w io.Writer) {
	path := a.GetDebugLogPath()
	if path == "" {
		fmt.Fprintln(w, "debug log is disabled or uninitialized.")
		return
	}
	info, err := os.Stat(path)
	sizeStr := "not created yet"
	if err == nil {
		sizeStr = fmt.Sprintf("%d bytes", info.Size())
	}
	fmt.Fprintf(w, "debug log file: %s (%s)\n", path, sizeStr)
	fmt.Fprintln(w, "records full LLM requests, function calls, arguments, outputs, and repetition statistics.")
}
