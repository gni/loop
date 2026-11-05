// Package run controls reasoning steps independently of terminal presentation,
// provider protocols and session storage. Main agents and subagents use it alike.
package run

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"loop/pkg/domain/message"
	"loop/pkg/transport/retry"
)

type Status string

const (
	Completed    Status = "completed"
	Failed       Status = "failed"
	Cancelled    Status = "cancelled"
	Blocked      Status = "blocked"
	LimitReached Status = "limit_reached"
)

var (
	ErrEmptyResponse = errors.New("provider returned no usable assistant response")
	ErrBlocked       = errors.New("agent execution blocked")
)

const (
	DefaultRequestAttempts  = 4
	DefaultRecoveryAttempts = 2
)

type Policy struct {
	MaxSteps         int
	Continuous       bool
	RequestAttempts  int
	RecoveryAttempts int
}

// Driver supplies side effects. Infer may stream presentation, but it cannot
// execute tools. Record must durably accept the assistant before Execute runs.
type Driver struct {
	Observe           func(Event) error
	Prepare           func(context.Context, int) error
	Infer             func(context.Context, int) (*message.Message, error)
	Record            func(context.Context, int, *message.Message) error
	Execute           func(context.Context, int, *message.Message) error
	Complete          func(context.Context, int, *message.Message) error
	Compact           func(context.Context) error
	IsContextOverflow func(error) bool
	IsRecoverable     func(error) bool
	Interrupted       func(context.Context, int, *message.Message, error) error
	Pause             func(context.Context, int, int) error
}

type Result struct {
	Status  Status
	Steps   int
	Message *message.Message
}

func HasUsableResponse(msg *message.Message) bool {
	if msg == nil {
		return false
	}
	if len(msg.ToolCalls) > 0 {
		return true
	}
	text := strings.ToLower(strings.TrimRight(strings.TrimSpace(msg.Content), ".!"))
	return text != "" && text != "no response from model" && text != "no response from the model"
}

func Execute(ctx context.Context, policy Policy, driver Driver) (result Result, runErr error) {
	result.Status = Completed
	defer func() {
		if runErr == nil {
			return
		}
		switch {
		case errors.Is(runErr, context.Canceled), errors.Is(runErr, context.DeadlineExceeded):
			result.Status = Cancelled
		case errors.Is(runErr, ErrBlocked):
			result.Status = Blocked
		case result.Status != LimitReached:
			result.Status = Failed
		}
	}()
	if driver.Infer == nil || driver.Record == nil || driver.Execute == nil {
		return result, errors.New("runner requires inference, recording and tool execution")
	}
	if policy.MaxSteps <= 0 {
		policy.MaxSteps = 30
	}
	if policy.RequestAttempts <= 0 {
		policy.RequestAttempts = DefaultRequestAttempts
	}
	observe := func(event Event) error {
		if driver.Observe == nil {
			return nil
		}
		return driver.Observe(event)
	}
	for step := 1; policy.Continuous || step <= policy.MaxSteps; step++ {
		result.Steps = step
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := observe(Event{Type: "turn_start", Step: step}); err != nil {
			return result, err
		}
		stepCtx := retry.WithBudget(ctx, policy.RequestAttempts)
		if driver.Prepare != nil {
			if err := driver.Prepare(stepCtx, step); err != nil {
				return result, err
			}
		}
		compacted := false
		recoveries := 0
		var msg *message.Message
		for {
			var err error
			if err := observe(Event{Type: "inference_start", Step: step}); err != nil {
				return result, err
			}
			msg, err = driver.Infer(stepCtx, step)
			if observeErr := observe(Event{Type: "inference_end", Step: step}); observeErr != nil {
				return result, errors.Join(err, observeErr)
			}
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			if err == nil && !HasUsableResponse(msg) {
				err = ErrEmptyResponse
			}
			if err == nil {
				break
			}
			if ctx.Err() == nil && !errors.Is(err, retry.ErrExhausted) && !compacted && driver.Compact != nil && driver.IsContextOverflow != nil && driver.IsContextOverflow(err) {
				compacted = true
				if compactErr := driver.Compact(stepCtx); compactErr != nil {
					return result, errors.Join(err, compactErr)
				}
				continue
			}
			if driver.Interrupted != nil {
				if recordErr := driver.Interrupted(stepCtx, step, msg, err); recordErr != nil {
					return result, errors.Join(err, recordErr)
				}
			}
			if ctx.Err() != nil || errors.Is(err, retry.ErrExhausted) || recoveries >= policy.RecoveryAttempts || driver.IsRecoverable == nil || !driver.IsRecoverable(err) {
				return result, err
			}
			recoveries++
			if driver.Pause != nil {
				if waitErr := driver.Pause(stepCtx, step, recoveries); waitErr != nil {
					return result, waitErr
				}
			} else {
				timer := time.NewTimer(time.Second * time.Duration(1<<min(recoveries-1, 5)))
				select {
				case <-stepCtx.Done():
					timer.Stop()
					return result, stepCtx.Err()
				case <-timer.C:
				}
			}
		}
		if err := driver.Record(ctx, step, msg); err != nil {
			return result, err
		}
		if len(msg.ToolCalls) == 0 {
			result.Message = msg
			if driver.Complete != nil {
				runErr = driver.Complete(ctx, step, msg)
			}
			if runErr == nil {
				runErr = observe(Event{Type: "turn_end", Step: step, Message: msg})
			}
			return result, runErr
		}
		if err := driver.Execute(ctx, step, msg); err != nil {
			return result, err
		}
		if err := observe(Event{Type: "turn_end", Step: step, Message: msg}); err != nil {
			return result, err
		}
		if driver.Pause != nil {
			if err := driver.Pause(stepCtx, step, 0); err != nil {
				return result, err
			}
		}
	}
	result.Status = LimitReached
	return result, fmt.Errorf("maximum reasoning steps reached (%d)", policy.MaxSteps)
}
