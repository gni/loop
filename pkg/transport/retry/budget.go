// Package retry bounds network attempts across inference, fallback and recovery.
package retry

import (
	"context"
	"errors"
	"sync/atomic"
)

var ErrExhausted = errors.New("inference request budget exhausted")

type budget struct{ remaining atomic.Int64 }
type contextKey struct{}

// WithBudget creates a budget for one reasoning step. It is shared by all
// requests derived from the context, including compaction and protocol fallback.
func WithBudget(ctx context.Context, attempts int) context.Context {
	b := &budget{}
	b.remaining.Store(int64(max(0, attempts)))
	return context.WithValue(ctx, contextKey{}, b)
}

// Check reports whether another attempt is available without reserving it.
func Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b, _ := ctx.Value(contextKey{}).(*budget); b != nil && b.remaining.Load() <= 0 {
		return ErrExhausted
	}
	return nil
}

// Claim reserves one network attempt. Calls outside a runner retain their
// transport's own retry limit.
func Claim(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b, ok := ctx.Value(contextKey{}).(*budget)
	if !ok {
		return nil
	}
	for remaining := b.remaining.Load(); remaining > 0; remaining = b.remaining.Load() {
		if b.remaining.CompareAndSwap(remaining, remaining-1) {
			return nil
		}
	}
	return ErrExhausted
}
