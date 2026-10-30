package style

import (
	"fmt"
	"strings"
	"time"
)

// FormatElapsedDuration uses larger units as a timer grows. Fractional seconds
// are optional and, when enabled, show exactly six digits of microsecond precision.
func FormatElapsedDuration(elapsed time.Duration, showMicroseconds bool) string {
	if elapsed < 0 {
		elapsed = 0
	}
	var parts []string
	if days := elapsed / (24 * time.Hour); days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours := elapsed / time.Hour % 24; hours > 0 || len(parts) > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes := elapsed / time.Minute % 60; minutes > 0 || len(parts) > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	seconds := elapsed / time.Second % 60
	if showMicroseconds {
		parts = append(parts, fmt.Sprintf("%d.%06ds", seconds, elapsed%time.Second/time.Microsecond))
	} else {
		parts = append(parts, fmt.Sprintf("%ds", seconds))
	}
	return strings.Join(parts, " ")
}
