package style

import (
	"testing"
	"time"
)

func TestFormatElapsedDuration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		elapsed time.Duration
		whole   string
		precise string
	}{
		{"zero", 0, "0s", "0.000000s"},
		{"negative", -time.Second, "0s", "0.000000s"},
		{"subsecond", 700 * time.Millisecond, "0s", "0.700000s"},
		{"before minute", time.Minute - time.Nanosecond, "59s", "59.999999s"},
		{"minute", time.Minute, "1m 0s", "1m 0.000000s"},
		{"reported example", 264*time.Second + 700*time.Millisecond, "4m 24s", "4m 24.700000s"},
		{"before hour", time.Hour - time.Nanosecond, "59m 59s", "59m 59.999999s"},
		{"hour", time.Hour, "1h 0m 0s", "1h 0m 0.000000s"},
		{"hours and minutes", time.Hour + 2*time.Minute + 3*time.Second + 123456*time.Microsecond, "1h 2m 3s", "1h 2m 3.123456s"},
		{"day", 24 * time.Hour, "1d 0h 0m 0s", "1d 0h 0m 0.000000s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatElapsedDuration(tc.elapsed, false); got != tc.whole {
				t.Errorf("whole seconds = %q; want %q", got, tc.whole)
			}
			if got := FormatElapsedDuration(tc.elapsed, true); got != tc.precise {
				t.Errorf("microseconds = %q; want %q", got, tc.precise)
			}
		})
	}
}
