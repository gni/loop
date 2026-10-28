package process

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type Options struct {
	Executable  string
	Arguments   []string
	Directory   string
	Environment []string
	Input       io.Reader
	Timeout     time.Duration
	OutputLimit int
	ErrorLimit  int
}

type Result struct {
	Stdout string
	Stderr string
}

type Buffer struct {
	data     bytes.Buffer
	Limit    int
	Overflow bool
}

func (b *Buffer) Write(data []byte) (int, error) {
	size := len(data)
	remaining := max(0, b.Limit-b.data.Len())
	if len(data) > remaining {
		b.Overflow = true
		data = data[:remaining]
	}
	_, _ = b.data.Write(data)
	return size, nil
}

func Run(ctx context.Context, options Options) (Result, error) {
	if options.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, options.Timeout)
		defer cancel()
	}
	output := &Buffer{Limit: options.OutputLimit}
	if output.Limit <= 0 {
		output.Limit = 1 << 20
	}
	diagnostics := &Buffer{Limit: options.ErrorLimit}
	if diagnostics.Limit <= 0 {
		diagnostics.Limit = 32 << 10
	}
	cmd := exec.CommandContext(ctx, options.Executable, options.Arguments...)
	cmd.Dir = options.Directory
	cmd.Env = options.Environment
	cmd.Stdin = options.Input
	cmd.Stdout = output
	cmd.Stderr = diagnostics
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	result := Result{Stdout: output.data.String(), Stderr: diagnostics.data.String()}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if output.Overflow {
		return result, fmt.Errorf("process output exceeded %d bytes", output.Limit)
	}
	if err != nil {
		return result, err
	}
	return result, nil
}
