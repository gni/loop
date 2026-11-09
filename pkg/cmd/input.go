package cmd

import (
	"context"
	"io"
)

// cancellableInput interrupts closable streams and checks cancellation between
// reads. The stop function leaves an input open after normal completion.
func cancellableInput(ctx context.Context, input io.Reader) (io.Reader, func()) {
	if fileInput, ok := cancellableFileInput(ctx, input); ok {
		return contextInput{ctx: ctx, input: fileInput}, func() {}
	}
	stop := func() {}
	if closer, ok := input.(io.Closer); ok && ctx.Done() != nil {
		stopClose := context.AfterFunc(ctx, func() { _ = closer.Close() })
		stop = func() { stopClose() }
	}
	return contextInput{ctx: ctx, input: input}, stop
}

type contextInput struct {
	ctx   context.Context
	input io.Reader
}

func (r contextInput) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.input.Read(p)
	if cancelled := r.ctx.Err(); cancelled != nil {
		return 0, cancelled
	}
	return n, err
}
