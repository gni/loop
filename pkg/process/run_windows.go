//go:build windows

package process

import (
	"errors"
	"os"
	"os/exec"
	"time"
)

func SetupProcessGroup(cmd *exec.Cmd) {
}

func KillProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	err := cmd.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func KillPgid(pgid int) error {
	return nil
}

func TerminateProcessGroup(cmd *exec.Cmd, grace time.Duration) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
