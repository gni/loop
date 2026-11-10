package bash

import (
	"context"
	"fmt"
	"os"
	"strings"

	"loop/pkg/storage"
)

type directoryCapture struct{ path string }

// Keep shell state out of stdout, including redirected and truncated output.
func captureDirectory(command string) (string, *directoryCapture, error) {
	file, err := os.CreateTemp("", "loop-cwd-*")
	if err != nil {
		return "", nil, fmt.Errorf("create shell directory capture: %w", err)
	}
	capture := &directoryCapture{path: file.Name()}
	if err := file.Close(); err != nil {
		capture.Close()
		return "", nil, err
	}
	trap := "builtin pwd -P >| " + ShellQuote(capture.path) + " || :"
	return "trap " + ShellQuote(trap) + " EXIT\n" + command, capture, nil
}

func (c *directoryCapture) Read(ctx context.Context) string {
	data, err := storage.ReadFileContext(ctx, c.path, 64<<10)
	if err != nil {
		return ""
	}
	// pwd adds exactly one newline. Other whitespace belongs to the path.
	return strings.TrimSuffix(string(data), "\n")
}

func (c *directoryCapture) Close() { _ = os.Remove(c.path) }
