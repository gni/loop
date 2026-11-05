//go:build windows

package cmd

import (
	"context"
	"io"
	"os"
)

func readTerminalKey(ctx context.Context, input *os.File) ([]byte, error) {
	var key []byte
	var b [1]byte
	n, err := input.Read(b[:])
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, io.EOF
	}
	key = append(key, b[0])
	return key, nil
}
