package process

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// StartCapture owns the output pipes so command completion can stop children
// before waiting for inherited descriptors to close. Call wait after success.
func StartCapture(cmd *exec.Cmd, stdout, stderr io.Writer) (wait func() error, err error) {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	outReader, outWriter, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	errReader, errWriter, err := os.Pipe()
	if err != nil {
		_ = outReader.Close()
		_ = outWriter.Close()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = outWriter, errWriter
	err = cmd.Start()
	_ = outWriter.Close()
	_ = errWriter.Close()
	if err != nil {
		_ = outReader.Close()
		_ = errReader.Close()
		return nil, err
	}
	copied := make(chan error, 2)
	copyOutput := func(w io.Writer, r io.Reader) {
		_, err := io.Copy(w, r)
		if err != nil {
			// A failed consumer must not leave the child blocked on a full pipe.
			_, _ = io.Copy(io.Discard, r)
		}
		copied <- err
	}
	go copyOutput(stdout, outReader)
	go copyOutput(stderr, errReader)
	var once sync.Once
	var result error
	return func() error {
		once.Do(func() {
			defer outReader.Close()
			defer errReader.Close()
			result = errors.Join(cmd.Wait(), KillProcessGroup(cmd))
			delay := cmd.WaitDelay
			if delay <= 0 {
				delay = 2 * time.Second
			}
			timer := time.NewTimer(delay)
			defer timer.Stop()
			for remaining := 2; remaining > 0; remaining-- {
				select {
				case copyErr := <-copied:
					result = errors.Join(result, copyErr)
				case <-timer.C:
					_ = outReader.Close()
					_ = errReader.Close()
					result = errors.Join(result, exec.ErrWaitDelay, <-copied)
				}
			}
		})
		return result
	}, nil
}
