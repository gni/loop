//go:build !windows

package process

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func SetupProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Detach child processes into a new session without a controlling terminal (/dev/tty)
	// while making the child the leader of its own process group (pgid == pid).
	// Interactive password/TTY readers (rpassword, getpass, sudo, ssh, gpg) fail fast
	// with ENXIO instead of blocking on /dev/tty or freezing on SIGTTIN/SIGTTOU.
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setpgid = false
	cmd.SysProcAttr.Pgid = 0
	if cmd.Cancel != nil {
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return os.ErrProcessDone
			}
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
}

func KillProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	groupErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if groupErr == syscall.ESRCH {
		groupErr = nil
	}
	processErr := cmd.Process.Kill()
	if errors.Is(processErr, os.ErrProcessDone) {
		processErr = nil
	}
	return errors.Join(groupErr, processErr)
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
