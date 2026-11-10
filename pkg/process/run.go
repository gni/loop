package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
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

// String returns the retained output, up to Limit bytes.
func (b *Buffer) String() string { return b.data.String() }

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
	SetupProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	err := captureCommand(cmd, output, diagnostics)
	result := Result{Stdout: output.String(), Stderr: diagnostics.String()}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if output.Overflow {
		err = errors.Join(err, fmt.Errorf("process output exceeded %d bytes", output.Limit))
	}
	if diagnostics.Overflow {
		err = errors.Join(err, fmt.Errorf("process diagnostics exceeded %d bytes", diagnostics.Limit))
	}
	return result, err
}

func captureCommand(cmd *exec.Cmd, stdout, stderr io.Writer) error {
	wait, err := StartCapture(cmd, stdout, stderr)
	if err != nil {
		return err
	}
	return wait()
}
