package interactive

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"loop/pkg/config"
	"loop/pkg/ui/style"
)

// RunNostopSetup commits neither choice until a valid interval is confirmed.
func RunNostopSetup(w io.Writer, theme style.UITheme) (config.NostopConfig, bool) {
	modes := []string{"eco", "balanced", "continuous"}
	selected, cancelled := runModalChoice(w, theme, " Choose nonstop pacing", []ChoiceMenuOption{
		{Label: "Eco: pause every tool turn, sequential tools", Keys: "1"},
		{Label: "Balanced: pause every five tool turns", Keys: "2"},
		{Label: "Continuous: pause between tool turns (0 = none)", Keys: "3"},
	}, 1)
	if cancelled {
		return config.NostopConfig{}, false
	}
	mode := modes[selected]
	minimum := 1
	if mode == "continuous" {
		minimum = 0
	}
	for {
		_, cancelled, text := runModalChoiceEx(w, theme,
			fmt.Sprintf(" Pause in seconds for %s (minimum %d; Esc cancels)", mode, minimum),
			[]ChoiceMenuOption{{FreeText: true}}, 0)
		if cancelled || strings.TrimSpace(text) == "" {
			return config.NostopConfig{}, false
		}
		seconds, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil {
			fmt.Fprintln(w, "Enter a whole number of seconds.")
			continue
		}
		next, err := config.NewNostopConfig(mode, seconds)
		if err != nil {
			fmt.Fprintln(w, err)
			continue
		}
		return next, true
	}
}
