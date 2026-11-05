//go:build windows

package bash

import (
	"os/exec"
)

func setupSysProcAttr(cmd *exec.Cmd) {
}

func killProcessGroup(pgid int) {
}

func extractSignalDiagnostic(exitErr *exec.ExitError) string {
	return ""
}
