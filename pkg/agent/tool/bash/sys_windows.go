//go:build windows

package bash

import (
	"os/exec"
)

func extractSignalDiagnostic(exitErr *exec.ExitError) string {
	return ""
}
