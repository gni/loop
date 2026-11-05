//go:build !windows

package process

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

func SetupProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func KillProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	return cmd.Process.Kill()
}

func KillPgid(pgid int) error {
	if pgid <= 0 {
		return nil
	}
	return syscall.Kill(-pgid, syscall.SIGKILL)
}

func TerminateProcessGroup(cmd *exec.Cmd, grace time.Duration) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	if grace > 0 {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		<-timer.C
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return cmd.Process.Kill()
}
