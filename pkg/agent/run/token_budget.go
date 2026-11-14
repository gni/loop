package run

import (
	"context"
	"errors"
	"sync"
)

var ErrTokenBudget = errors.New("task token budget exhausted")

type requestBudgetKey struct{}
type totalBudgetKey struct{}
type totalBudget struct {
	sync.Mutex
	remaining int
}
type requestBudget struct {
	sync.Mutex
	limit, input, output int
	spent                int
	usageRecorded        bool
}

func withTotalTokenBudget(ctx context.Context, limit int) context.Context {
	return context.WithValue(ctx, totalBudgetKey{}, &totalBudget{remaining: limit})
}

// EnsureRequestBudget gives every inference, including compaction, its own
// allocation within the same task-wide token allowance.
func EnsureRequestBudget(ctx context.Context) (context.Context, error) {
	limit := RequestTokenLimit(ctx)
	if limit < 0 {
		return ctx, ErrTokenBudget
	}
	if current, _ := ctx.Value(requestBudgetKey{}).(*requestBudget); current == nil && limit > 0 {
		ctx = WithRequestBudget(ctx, limit)
	}
	return ctx, nil
}

func WithRequestBudget(ctx context.Context, limit int) context.Context {
	if limit <= 0 {
		return ctx
	}
	return context.WithValue(ctx, requestBudgetKey{}, &requestBudget{limit: limit})
}

func RequestTokenLimit(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	limit := 0
	if budget, _ := ctx.Value(requestBudgetKey{}).(*requestBudget); budget != nil {
		limit = budget.limit
	}
	if total, _ := ctx.Value(totalBudgetKey{}).(*totalBudget); total != nil {
		total.Lock()
		remaining := total.remaining
		total.Unlock()
		if remaining <= 0 {
			return -1
		}
		if limit == 0 || remaining < limit {
			limit = remaining
		}
	}
	return limit
}

func RecordRequestBudget(ctx context.Context, input, output int) {
	if ctx == nil {
		return
	}
	budget, _ := ctx.Value(requestBudgetKey{}).(*requestBudget)
	if budget == nil {
		return
	}
	budget.Lock()
	budget.input, budget.output = max(0, input), max(0, output)
	budget.Unlock()
}

func allocatedRequestTokens(ctx context.Context) (int, int) {
	budget, _ := ctx.Value(requestBudgetKey{}).(*requestBudget)
	if budget == nil {
		return 0, 0
	}
	budget.Lock()
	defer budget.Unlock()
	return budget.input, budget.output
}

// RecordRequestAttempt reserves the whole allocation for each HTTP retry, whose
// remote usage can be unknown. Optional tokenizer requests are not generation.
func RecordRequestAttempt(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	budget, _ := ctx.Value(requestBudgetKey{}).(*requestBudget)
	if budget == nil {
		return nil
	}
	budget.Lock()
	defer budget.Unlock()
	if budget.input > budget.limit || budget.output > budget.limit-budget.input {
		return ErrTokenBudget
	}
	cost := max(1, budget.input+budget.output)
	if cost > budget.limit-budget.spent {
		return ErrTokenBudget
	}
	if total, _ := ctx.Value(totalBudgetKey{}).(*totalBudget); total != nil {
		total.Lock()
		defer total.Unlock()
		if cost > total.remaining {
			return ErrTokenBudget
		}
		total.remaining -= cost
	}
	budget.spent += cost
	return nil
}

// RecordRequestUsage refunds only known unused tokens on the final attempt.
// Failed attempts retain their conservative reservation when usage is unknown.
func RecordRequestUsage(ctx context.Context, input, output int) {
	if input <= 0 || output <= 0 {
		return
	}
	budget, _ := ctx.Value(requestBudgetKey{}).(*requestBudget)
	if budget == nil {
		return
	}
	budget.Lock()
	defer budget.Unlock()
	total, _ := ctx.Value(totalBudgetKey{}).(*totalBudget)
	if total == nil {
		return
	}
	if budget.usageRecorded {
		return
	}
	budget.usageRecorded = true
	total.Lock()
	defer total.Unlock()
	capacity := total.remaining
	if budget.spent > 0 {
		capacity += budget.input + budget.output
	}
	if input > capacity || output > capacity-input {
		total.remaining = 0
		return
	}
	total.remaining = capacity - input - output
}
