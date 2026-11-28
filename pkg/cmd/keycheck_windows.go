//go:build windows

package cmd

import (
	"context"
	"io"
	"os"
)

func readTerminalKey(ctx context.Context, input *os.File) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var key []byte
	for len(key) < 64 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		type readRes struct {
			data []byte
			err  error
		}
		ch := make(chan readRes, 1)
		go func(limit int) {
			buf := make([]byte, limit)
			n, err := input.Read(buf)
			ch <- readRes{data: buf[:n], err: err}
		}(64 - len(key))

		var res readRes
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case res = <-ch:
		}
		if res.err != nil {
			return nil, res.err
		}
		if len(res.data) == 0 {
			if len(key) > 0 {
				return key, nil
			}
			return nil, io.EOF
		}
		key = append(key, res.data...)
		if len(key) == 1 {
			if key[0] == 3 || key[0] == 4 {
				return nil, context.Canceled
			}
			return key, nil
		}
		if key[0] != 27 {
			return key, nil
		}
		if key[1] == '[' {
			for i := 2; i < len(key); i++ {
				if key[i] >= 0x40 && key[i] <= 0x7e {
					if string(key[:i+1]) == "\x1b[99;5u" || string(key[:i+1]) == "\x1b[100;5u" {
						return nil, context.Canceled
					}
					return key[:i+1], nil
				}
			}
			continue
		}
		if key[1] == 'O' {
			if len(key) >= 3 {
				return key[:3], nil
			}
			continue
		}
		return key, nil
	}
	return key, nil
}
