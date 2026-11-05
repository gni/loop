//go:build !windows

package cmd

import (
	"context"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func readTerminalKey(ctx context.Context, input *os.File) ([]byte, error) {
	var key []byte
	for len(key) < 64 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fds := []unix.PollFd{{Fd: int32(input.Fd()), Events: unix.POLLIN}}
		ready, err := unix.Poll(fds, 100)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, err
		}
		if ready == 0 {
			if len(key) > 0 {
				return key, nil
			}
			continue
		}
		var b [1]byte
		n, err := input.Read(b[:])
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, io.EOF
		}
		key = append(key, b[0])
		if len(key) == 1 {
			if b[0] == 3 || b[0] == 4 {
				return nil, context.Canceled
			}
			if b[0] != 27 {
				return key, nil
			}
		} else if len(key) == 2 {
			if b[0] != '[' && b[0] != 'O' {
				return key, nil
			}
		} else if b[0] >= 0x40 && b[0] <= 0x7e {
			if string(key) == "\x1b[99;5u" || string(key) == "\x1b[100;5u" {
				return nil, context.Canceled
			}
			return key, nil
		}
	}
	return key, nil
}
