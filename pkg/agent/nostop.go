package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"loop/pkg/db"
	transporthttp "loop/pkg/transport/http"
	"loop/pkg/ui/style"
)

var (
	ErrEmptyResponse      = errors.New("provider returned no usable assistant response")
	ErrIncompleteResponse = errors.New("provider response is incomplete")
	responseHTTPStatus    = regexp.MustCompile(`non-200 status: (\d{3})\b`)
)

func hasUsableAssistantResponse(msg *db.Message) bool {
	if msg == nil {
		return false
	}
	if len(msg.ToolCalls) > 0 {
		return true
	}
	text := strings.TrimSpace(msg.Content)
	switch strings.ToLower(strings.TrimRight(text, ".!")) {
	case "", "no response from model", "no response from the model":
		return false
	}
	return true
}

// No total-duration or inactivity deadline is imposed by Loop in nostop mode.
// Cancellation still propagates to the HTTP request and active tools.
func (p *OpenAICompatibleProvider) streamTimeout() time.Duration {
	if p.Config == nil || p.Config.Nostop.Enabled() || p.Config.Timeout <= 0 {
		return 0
	}
	return time.Duration(p.Config.Timeout) * time.Second
}

func recoverableNostopError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || transporthttp.IsNonRetryableError(err) {
		return false
	}
	if errors.Is(err, ErrEmptyResponse) || errors.Is(err, ErrIncompleteResponse) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	if match := responseHTTPStatus.FindStringSubmatch(err.Error()); len(match) == 2 {
		code, _ := strconv.Atoi(match[1])
		return transporthttp.IsRetryableStatus(code)
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"http request failed:", "rate limit", "temporarily unavailable", "overloaded"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (a *Agent) pauseNostop(ctx context.Context, w io.Writer, loader *turnLoader, sessionID string, theme style.UITheme, failures int) error {
	delay := a.Config.Nostop.Pause()
	if failures > 0 {
		// Failed requests always back off, even when the chosen pause is zero.
		delay = max(delay, min(60*time.Second, time.Second*time.Duration(1<<min(failures-1, 6))))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay == 0 {
		return nil
	}
	if loader != nil {
		loader.Pause()
		defer func() {
			if ctx.Err() == nil {
				loader.Resume()
			}
		}()
	}
	label := fmt.Sprintf("↻ %s · pause %s · Ctrl+C to stop", a.Config.Nostop.Mode, delay)
	if failures > 0 {
		label = "recovering · " + label
	}
	a.emit(Event{Type: "nostop_wait", SessionID: sessionID, Output: label, Data: map[string]any{"mode": a.Config.Nostop.Mode, "seconds": delay.Seconds(), "recovering": failures > 0}})
	if a.UI != nil {
		if activity, ok := a.UI.(interface {
			DrawActivityStatsLine(io.Writer, style.UITheme, string)
		}); ok {
			activity.DrawActivityStatsLine(UnwrapWriter(w), theme, label)
			defer activity.DrawActivityStatsLine(UnwrapWriter(w), theme, "")
		} else {
			// A nonempty activity frame keeps the label out of saved stats.
			a.UI.DrawStatsLine(UnwrapWriter(w), theme, "↻", strings.TrimPrefix(label, "↻ "))
			defer a.UI.DrawStatsLine(UnwrapWriter(w), theme, "", "")
		}
	} else {
		fmt.Fprintln(w, label)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
