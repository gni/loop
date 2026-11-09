//go:build !windows

package cmd

import (
	"context"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func cancellableFileInput(ctx context.Context, input io.Reader) (io.Reader, bool) {
	file, ok := input.(*os.File)
	if !ok || ctx.Done() == nil {
		return nil, false
	}
	return polledFileInput{ctx: ctx, file: file, fd: int32(file.Fd())}, true
}

type polledFileInput struct {
	ctx  context.Context
	file *os.File
	fd   int32
}

func (r polledFileInput) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.fd < 0 {
		return 0, os.ErrClosed
	}
	// Standard input may use a blocking descriptor, where Close cannot
	// interrupt an active read. Wait for readiness without changing its flags.
	fds := []unix.PollFd{{Fd: r.fd, Events: unix.POLLIN}}
	for {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		ready, err := unix.Poll(fds, 50)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		if ready > 0 {
			return r.file.Read(p)
		}
	}
}
