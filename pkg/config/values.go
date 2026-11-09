package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

func ParseHeaderArgs(args []string) map[string]string {
	headers := make(map[string]string)
	for _, arg := range args {
		parts := strings.SplitN(arg, ":", 2)
		if len(parts) != 2 {
			parts = strings.SplitN(arg, "=", 2)
		}
		if len(parts) == 2 {
			headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return headers
}

func ParseBool(val string) bool {
	value, _ := ParseBoolValue(val)
	return value
}

func ParseReasoningEffort(val string) (string, error) {
	switch v := strings.ToLower(strings.TrimSpace(val)); v {
	case "off", "none", "false", "0":
		return "off", nil
	case "low", "medium", "high", "max":
		return v, nil
	default:
		return "", fmt.Errorf("invalid reasoning effort '%s'. Allowed: off, low, medium, high, max", val)
	}
}

func ParseAskUserMode(val string) (string, error) {
	switch mode := strings.ToLower(strings.TrimSpace(val)); mode {
	case "interactive", "enabled", "enable", "on", "true", "1":
		return "interactive", nil
	case "disabled", "disable", "off", "false", "0":
		return "disabled", nil
	case "always_ask", "auto_recommended":
		return mode, nil
	default:
		return "", fmt.Errorf("invalid ask_user_mode '%s'. Allowed: interactive (on), always_ask, auto_recommended, disabled (off)", val)
	}
}

func ParseCompressionThreshold(val string) (float64, error) {
	v := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(val), "%"))
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
		return 0, fmt.Errorf("must be a positive number (e.g. 0.90 or 90%%)")
	}
	if f > 1.0 {
		f /= 100.0
	}
	if f > 0.98 {
		f = 0.98
	}
	return f, nil
}

func ParseToolList(val string) []string {
	trimmed := strings.TrimSpace(val)
	switch strings.ToLower(trimmed) {
	case "", "none", "clear", "reset", "empty":
		return nil
	}
	var result []string
	for _, p := range strings.Split(trimmed, ",") {
		if t := strings.TrimSpace(p); t != "" {
			result = append(result, t)
		}
	}
	return result
}

func ParseBoolValue(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("expected true or false, got %q", value)
	}
}
