package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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

// Own the capture pipes so descendants are stopped as soon as the command
// exits, before waiting for their inherited output descriptors to close.
func captureCommand(cmd *exec.Cmd, stdout, stderr io.Writer) error {
	outReader, outWriter, err := os.Pipe()
	if err != nil {
		return err
	}
	defer outReader.Close()
	defer outWriter.Close()
	errReader, errWriter, err := os.Pipe()
	if err != nil {
		return err
	}
	defer errReader.Close()
	defer errWriter.Close()
	cmd.Stdout, cmd.Stderr = outWriter, errWriter
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = outWriter.Close()
	_ = errWriter.Close()
	copied := make(chan error, 2)
	go func() { _, err := io.Copy(stdout, outReader); copied <- err }()
	go func() { _, err := io.Copy(stderr, errReader); copied <- err }()
	err = errors.Join(cmd.Wait(), KillProcessGroup(cmd))
	timer := time.NewTimer(cmd.WaitDelay)
	defer timer.Stop()
	for remaining := 2; remaining > 0; remaining-- {
		select {
		case copyErr := <-copied:
			err = errors.Join(err, copyErr)
		case <-timer.C:
			// A detached descendant can escape the group. It must not retain
			// our readers or keep the caller waiting indefinitely.
			_ = outReader.Close()
			_ = errReader.Close()
			err = errors.Join(err, exec.ErrWaitDelay, <-copied)
		}
	}
	return err
}
