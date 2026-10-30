package config

import (
	"fmt"
	"strings"
	"time"
)

// NostopConfig requires an explicit pause, including an explicit zero for
// continuous mode. A missing pause must never start an unattended loop.
type NostopConfig struct {
	Mode            string `json:"mode,omitempty"`
	IntervalSeconds *int   `json:"interval_seconds,omitempty"`
}

func NewNostopConfig(mode string, seconds int) (NostopConfig, error) {
	n := NostopConfig{Mode: strings.ToLower(strings.TrimSpace(mode)), IntervalSeconds: &seconds}
	if n.Mode == "" {
		return n, fmt.Errorf("nostop mode must be eco, balanced, continuous, or off")
	}
	if n.Mode == "off" {
		n.IntervalSeconds = nil
	}
	return n, n.Validate()
}

func (n NostopConfig) Enabled() bool {
	return n.Mode != "" && n.Mode != "off"
}

func (n NostopConfig) Validate() error {
	if !n.Enabled() {
		return nil
	}
	switch n.Mode {
	case "eco", "balanced", "continuous":
	default:
		return fmt.Errorf("nostop mode must be eco, balanced, continuous, or off")
	}
	if n.IntervalSeconds == nil {
		return fmt.Errorf("nostop requires a pause in seconds; use /nostop to configure it or --nostop <mode> --nostop-interval <seconds>")
	}
	seconds := *n.IntervalSeconds
	if seconds < 0 || int64(seconds) > int64((1<<63-1)/time.Second) {
		return fmt.Errorf("nostop pause must be a nonnegative number of seconds within the supported duration range")
	}
	if seconds == 0 && n.Mode != "continuous" {
		return fmt.Errorf("%s mode requires a pause of at least 1 second; choose continuous for a zero-second pause", n.Mode)
	}
	return nil
}

func (n NostopConfig) PauseAfter(turn int) bool {
	return n.Mode == "eco" || n.Mode == "continuous" || (n.Mode == "balanced" && turn%5 == 0)
}

func (n NostopConfig) Pause() time.Duration {
	if n.IntervalSeconds == nil {
		return 0
	}
	return time.Duration(*n.IntervalSeconds) * time.Second
}
