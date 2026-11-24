package style

import (
	"math/bits"
	"strings"
	"testing"
)

func TestActivitySpinnerFixedWidthSmoothAndNoEmoji(t *testing.T) {
	themes := []string{"studio", "ashen", "everforest", "amber-crt", "paper", "light"}
	for _, name := range themes {
		theme := GetTheme(name)
		idle := ActivitySpinnerIdle(theme)
		if w := CellWidth(idle); w != 4 {
			t.Fatalf("theme %q idle width = %d, want 4", name, w)
		}
		if strings.ContainsRune(idle, '\uFE0E') {
			t.Fatalf("theme %q idle contains emoji presentation selector", name)
		}

		var prevPlain string
		seen := make(map[string]bool)
		for f := 0; f < 16; f++ {
			frame := ActivitySpinnerFrame(theme, f)
			if w := CellWidth(frame); w != 4 {
				t.Fatalf("theme %q frame %d width = %d, want 4 (plain=%q)", name, f, w, StripAnsi(frame))
			}
			plain := StripAnsi(frame)
			if prevPlain != "" && plain == prevPlain {
				t.Fatalf("theme %q stutter detected: frame %d repeated %q", name, f, plain)
			}
			prevPlain = plain
			seen[plain] = true

			// Every cell must contain exactly 2 dots (one per subpixel column, continuous ribbon).
			for _, r := range plain {
				if r < 0x2800 || r > 0x28FF {
					t.Fatalf("theme %q frame %d contains non-Braille rune %U", name, f, r)
				}
				if gotDots := bits.OnesCount(uint(r - 0x2800)); gotDots != 2 {
					t.Fatalf("theme %q frame %d rune %q has %d dots, want 2", name, f, r, gotDots)
				}
			}
		}
		if len(seen) != 8 {
			t.Fatalf("theme %q expected 8 distinct wave frames, got %d", name, len(seen))
		}
		// Negative frame index must clamp safely.
		if w := CellWidth(ActivitySpinnerFrame(theme, -3)); w != 4 {
			t.Fatalf("theme %q negative frame width = %d, want 4", name, w)
		}
	}
}
