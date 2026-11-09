//go:build windows

package cmd

import (
	"context"
	"io"
)

func cancellableFileInput(context.Context, io.Reader) (io.Reader, bool) {
	return nil, false
}
