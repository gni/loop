package agent

import (
	"context"
	"errors"
	"net"
	"regexp"

	"loop/pkg/agent/run"
	transporthttp "loop/pkg/transport/http"
	"loop/pkg/transport/retry"
)

var (
	overflowRegexes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)prompt (?:is )?too long`),
		regexp.MustCompile(`(?i)prompt exceeds max length`),
		regexp.MustCompile(`(?i)request_too_large`),
		regexp.MustCompile(`(?i)input is too long`),
		regexp.MustCompile(`(?i)exceeds (?:the )?(?:model'?s )?context window`),
		regexp.MustCompile(`(?i)exceeds (?:the )?(?:model'?s )?maximum context length`),
		regexp.MustCompile(`(?i)context window exceeded`),
		regexp.MustCompile(`(?i)input token count.*exceeds`),
		regexp.MustCompile(`(?i)maximum prompt length is \d+`),
		regexp.MustCompile(`(?i)reduce the length of the messages`),
		regexp.MustCompile(`(?i)maximum context length is \d+ tokens`),
		regexp.MustCompile(`(?i)maximum allowed input length`),
		regexp.MustCompile(`(?i)exceeds the available context size`),
		regexp.MustCompile(`(?i)context window exceeds limit`),
		regexp.MustCompile(`(?i)exceeded model token limit`),
		regexp.MustCompile(`(?i)too large for model with \d+ maximum context length`),
		regexp.MustCompile(`(?i)model_context_window_exceeded`),
		regexp.MustCompile(`(?i)range of input length should be`),
		regexp.MustCompile(`(?i)context[_ ]length[_ ]exceeded`),
		regexp.MustCompile(`(?i)token limit exceeded`),
		regexp.MustCompile(`(?i)(?:^|[\s:])413(?:[\s:]|$)`),
	}
	nonOverflowRegexes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)rate limit`),
		regexp.MustCompile(`(?i)too many requests`),
		regexp.MustCompile(`(?i)throttling`),
	}
)

func IsContextOverflow(err error) bool {
	return isContextOverflow(err)
}

func isContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	var failure *run.InferenceFailure
	if errors.As(err, &failure) {
		return failure.Kind == run.ContextOverflow
	}
	text := err.Error()
	for _, non := range nonOverflowRegexes {
		if non.MatchString(text) {
			return false
		}
	}
	for _, rx := range overflowRegexes {
		if rx.MatchString(text) {
			return true
		}
	}
	return false
}

// Provider compatibility diagnostics are classified once, at the adapter edge.
// The runner then uses typed failures rather than provider-specific wording.
func classifyInferenceFailure(err error) error {
	if err == nil {
		return nil
	}
	var existing *run.InferenceFailure
	if errors.As(err, &existing) {
		return err
	}
	kind := run.PermanentFailure
	var status *transporthttp.StatusError
	var network net.Error
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, retry.ErrExhausted):
	case errors.As(err, &status):
		if status.Code == 413 || (!transporthttp.IsRetryableStatus(status.Code) && isContextOverflow(err)) {
			kind = run.ContextOverflow
		} else if transporthttp.IsRetryableStatus(status.Code) {
			kind = run.TransientFailure
		}
	case isContextOverflow(err):
		kind = run.ContextOverflow
	case !transporthttp.IsNonRetryableError(err) && (errors.As(err, &network) || isRecoverableInferenceError(err)):
		kind = run.TransientFailure
	}
	return &run.InferenceFailure{Kind: kind, Cause: err}
}
