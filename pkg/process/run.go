package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
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
	Stdout   string
	Stderr   string
	Started  bool
	ExitCode *int
}

// Buffer drains output while retaining at most Limit bytes. Configure TailBytes
// before writing to retain recent diagnostics as well as the beginning.
type Buffer struct {
	mu    sync.Mutex
	data  bytes.Buffer
	tail  bytes.Buffer
	Limit int
	// TailBytes reserves recent output within Limit; zero keeps prefix capture.
	TailBytes int
	Overflow  bool
}

// BestEffortWriter keeps optional display errors out of command capture.
type BestEffortWriter struct{ io.Writer }

func (w BestEffortWriter) Write(data []byte) (int, error) {
	if w.Writer != nil {
		_, _ = w.Writer.Write(data)
	}
	return len(data), nil
}

func (b *Buffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	size := len(data)
	if b.TailBytes > 0 && b.Limit > 0 {
		tailLimit := min(b.TailBytes, b.Limit)
		headSize := min(len(data), max(0, b.Limit-tailLimit-b.data.Len()))
		_, _ = b.data.Write(data[:headSize])
		data = data[headSize:]
		if len(data) > tailLimit-b.tail.Len() {
			b.Overflow = true
		}
		if len(data) >= tailLimit {
			b.tail.Reset()
			data = data[len(data)-tailLimit:]
		} else if drop := b.tail.Len() + len(data) - tailLimit; drop > 0 {
			b.tail.Next(drop)
		}
		_, _ = b.tail.Write(data)
		return size, nil
	}
	remaining := max(0, b.Limit-b.data.Len())
	if len(data) > remaining {
		b.Overflow = true
		data = data[:remaining]
	}
	_, _ = b.data.Write(data)
	return size, nil
}

// String returns the retained output, up to Limit bytes.
func (b *Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tail.Len() == 0 {
		return b.data.String()
	}
	head, tail := b.data.Bytes(), b.tail.Bytes()
	const notice = "\n[... output omitted ...]\n"
	if b.Overflow && b.Limit > len(notice) {
		tailSize := min(len(tail), b.Limit-len(notice))
		headSize := min(len(head), b.Limit-len(notice)-tailSize)
		return string(head[:headSize]) + notice + string(tail[len(tail)-tailSize:])
	}
	return string(head) + string(tail)
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
	diagnostics.TailBytes = min(4096, diagnostics.Limit/4)
	cmd := exec.CommandContext(ctx, options.Executable, options.Arguments...)
	cmd.Dir = options.Directory
	cmd.Env = options.Environment
	cmd.Stdin = options.Input
	SetupProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	err := captureCommand(cmd, output, diagnostics)
	result := Result{Stdout: output.String(), Stderr: diagnostics.String(), Started: cmd.Process != nil}
	if cmd.ProcessState != nil {
		if code := cmd.ProcessState.ExitCode(); code >= 0 {
			result.ExitCode = &code
		}
	}
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
