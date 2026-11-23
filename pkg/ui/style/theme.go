package style

import (
	"fmt"
	"image/color"
	"strings"
)

type UITheme struct {
	// Brand & Selection
	Primary   color.Color // Focused titles, active tabs, primary actions
	Secondary color.Color // Auxiliary indicators, breadcrumbs
	Highlight color.Color // Search matches, cursor highlights, badges

	// Surface & Text Hierarchy
	Text       color.Color // Primary readable content
	TextMuted  color.Color // Dimmed content, metadata, hotkey hints
	Background color.Color // Surface background (if controlling canvas)

	// Borders & Dividers
	Border         color.Color // Standard/subdued border (legacy compatibility)
	BorderActive   color.Color // Focused pane border
	BorderInactive color.Color // Unfocused pane borders and column rules

	// Functional Status
	Success color.Color // OK, clean state, added lines
	Warning color.Color // Modifying, uncommitted, cautionary thresholds
	Error   color.Color // Failures, deletions, blocking states

	// Syntax Highlighting Profile
	ChromaStyle string
}

func makeTheme(
	primary, secondary, highlight,
	text, textMuted, bg,
	border, borderActive, borderInactive,
	success, warning, errColor, chroma string,
) UITheme {
	if borderInactive == "" {
		borderInactive = border
	}
	return UITheme{
		Primary:        Color(primary),
		Secondary:      Color(secondary),
		Highlight:      Color(highlight),
		Text:           Color(text),
		TextMuted:      Color(textMuted),
		Background:     Color(bg),
		Border:         Color(border),
		BorderActive:   Color(borderActive),
		BorderInactive: Color(borderInactive),
		Success:        Color(success),
		Warning:        Color(warning),
		Error:          Color(errColor),
		ChromaStyle:    chroma,
	}
}

func GetTheme(themeName string) UITheme {
	switch strings.ToLower(strings.TrimSpace(themeName)) {
	case "catppuccin", "catppuccin-mocha":
		return makeTheme(
			"#89B4FA", "#B4BEFE", "#F9E2AF",
			"#CDD6F4", "#9399B2", "#1E1E2E",
			"#45475A", "#89B4FA", "",
			"#A6E3A1", "#FAB387", "#F38BA8",
			"catppuccin-mocha",
		)
	case "tokyonight", "tokyo-night", "neon":
		return makeTheme(
			"#7AA2F7", "#7DCFFF", "#E0AF68",
			"#C0CAF5", "#7884B0", "#1A1B26",
			"#3B4261", "#7AA2F7", "",
			"#9ECE6A", "#E0AF68", "#F7768E",
			"tokyonight-night",
		)
	case "everforest":
		return makeTheme(
			"#83C092", "#7FBBB3", "#DBBC7F",
			"#D3C6AA", "#9DA9A0", "#272E33",
			"#495156", "#83C092", "",
			"#A7C080", "#E69875", "#E67E80",
			"evergarden",
		)
	case "gruvbox":
		return makeTheme(
			"#83A598", "#8EC07C", "#FABD2F",
			"#EBDBB2", "#A89984", "#282828",
			"#504945", "#83A598", "",
			"#B8BB26", "#FE8019", "#FB4934",
			"gruvbox",
		)
	case "mono", "plain", "minimal":
		return makeTheme(
			"#E5E7EB", "#B4BAC4", "#F8FAFC",
			"#D8DCE2", "#8B929E", "#111317",
			"#3A3F47", "#CBD5E1", "",
			"#86B89A", "#D9B77A", "#D97B82",
			"ashen",
		)
	case "light":
		return makeTheme(
			"#0369A1", "#4F46E5", "#B45309",
			"#1E293B", "#64748B", "#FFFFFF",
			"#CBD5E1", "#0369A1", "#E2E8F0",
			"#15803D", "#B45309", "#BE123C",
			"catppuccin-latte",
		)
	case "kanagawa", "kanagawa-wave":
		return makeTheme(
			"#7E9CD8", "#7FB4CA", "#E6C384",
			"#DCD7BA", "#8E8D82", "#1F1F28",
			"#3D3D4E", "#7E9CD8", "",
			"#98BB6C", "#FFA066", "#E46876",
			"kanagawa-wave",
		)
	case "rose-pine", "rose-pine-moon", "rosepine":
		return makeTheme(
			"#9CCFD8", "#C4A7E7", "#F6C177",
			"#E0DEF4", "#908CAA", "#232136",
			"#44415A", "#9CCFD8", "",
			"#87C7A8", "#F6C177", "#EB6F92",
			"rose-pine-moon",
		)
	case "zenburn", "earth-calm", "earth":
		return makeTheme(
			"#8CD0D3", "#94BFF3", "#F0DFAF",
			"#DCDCCC", "#9F9F92", "#2B2B2B",
			"#4F4F4F", "#8CD0D3", "",
			"#7F9F7F", "#DFAF8F", "#CC9393",
			"evergarden",
		)
	case "nord", "nord-calm":
		return makeTheme(
			"#88C0D0", "#81A1C1", "#EBCB8B",
			"#E5E9F0", "#8896AE", "#2E3440",
			"#4C566A", "#88C0D0", "",
			"#A3BE8C", "#D08770", "#BF616A",
			"nord",
		)
	case "studio", "dark", "default", "":
		fallthrough
	default:
		return makeTheme(
			"#7DD3FC", "#A5B4FC", "#FCD34D",
			"#E2E8F0", "#94A3B8", "#0F172A",
			"#334155", "#7DD3FC", "",
			"#6EE7B7", "#FBBF24", "#FB7185",
			"github-dark",
		)
	}
}

func normalizeChromaStyle(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "everforest":
		return "evergarden"
	case "kanagawa":
		return "kanagawa-wave"
	case "tokyo-night", "tokyonight":
		return "tokyonight-night"
	case "catppuccin":
		return "catppuccin-mocha"
	case "rosepine":
		return "rose-pine-moon"
	default:
		return strings.TrimSpace(name)
	}
}

// ResolveConfiguredTheme returns the UITheme configured by theme name and syntax highlighting theme.
func ResolveConfiguredTheme(themeName, syntaxTheme string) UITheme {
	theme := GetTheme(themeName)
	syn := strings.TrimSpace(syntaxTheme)
	if syn != "" && !strings.EqualFold(syn, "auto") {
		theme.ChromaStyle = normalizeChromaStyle(syn)
	}
	return theme
}

// GetThemeNames returns all supported UI theme identifiers.
func GetThemeNames() []string {
	return []string{
		"studio",
		"catppuccin-mocha",
		"tokyo-night",
		"everforest",
		"kanagawa",
		"rose-pine",
		"gruvbox",
		"nord",
		"minimal",
		"zenburn",
		"light",
	}
}

// CanonicalThemeName maps any recognized theme name or alias to its canonical
// identifier in GetThemeNames. Returns "" for unknown names.
func CanonicalThemeName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "default", "dark", "studio":
		return "studio"
	case "catppuccin", "catppuccin-mocha":
		return "catppuccin-mocha"
	case "tokyonight", "tokyo-night", "neon":
		return "tokyo-night"
	case "everforest":
		return "everforest"
	case "kanagawa", "kanagawa-wave":
		return "kanagawa"
	case "rose-pine", "rose-pine-moon", "rosepine":
		return "rose-pine"
	case "gruvbox":
		return "gruvbox"
	case "nord", "nord-calm":
		return "nord"
	case "minimal", "mono", "plain":
		return "minimal"
	case "zenburn", "earth-calm", "earth":
		return "zenburn"
	case "light":
		return "light"
	default:
		return ""
	}
}

// NextThemeName cycles forward (step > 0) or backward (step < 0) through GetThemeNames.
func NextThemeName(current string, step int) string {
	names := GetThemeNames()
	if len(names) == 0 {
		return "studio"
	}
	if step == 0 {
		step = 1
	}
	canon := CanonicalThemeName(current)
	idx := 0
	for i, n := range names {
		if n == canon {
			idx = i
			break
		}
	}
	next := (idx + step) % len(names)
	if next < 0 {
		next += len(names)
	}
	return names[next]
}

// ThemeDescription returns a brief visual description for a built-in theme.
func ThemeDescription(name string) string {
	switch CanonicalThemeName(name) {
	case "studio":
		return "crisp dark slate"
	case "catppuccin-mocha":
		return "warm pastel dark"
	case "tokyo-night":
		return "indigo twilight dark"
	case "everforest":
		return "soft forest sage"
	case "kanagawa":
		return "ink & wave indigo"
	case "rose-pine":
		return "muted plum & pine"
	case "gruvbox":
		return "retro warm earth"
	case "nord":
		return "arctic frost slate"
	case "minimal":
		return "monochrome low-chrome"
	case "zenburn":
		return "low-contrast warm charcoal"
	case "light":
		return "clean daylight canvas"
	default:
		return "custom palette"
	}
}

// GetSyntaxThemeNames returns standard supported chroma syntax highlighting styles.
func GetSyntaxThemeNames() []string {
	return []string{
		"auto",
		"github-dark",
		"catppuccin-mocha",
		"tokyonight-night",
		"evergarden",
		"kanagawa-wave",
		"rose-pine-moon",
		"gruvbox",
		"nord",
		"nordic",
		"onedark",
		"ashen",
		"catppuccin-latte",
		"solarized-light",
		"dracula",
		"monokai",
		"bw",
	}
}

// PromptStyle returns the style for input prompts.
func (t UITheme) PromptStyle() Style {
	return NewStyle().Foreground(t.Primary).Bold(true)
}

// FormatPromptWithQueue formats the prompt prefix with optional queue indicator and theme colors.
func FormatPromptWithQueue(theme UITheme, promptPrefix string, qLen int) (promptStr, fullPrefixPlain string) {
	queuePrefix := ""
	fullPrefixPlain = promptPrefix
	if qLen > 0 {
		queuePrefix = NewStyle().Foreground(theme.Highlight).Bold(true).Render(fmt.Sprintf("[queue: %d] ", qLen))
		fullPrefixPlain = fmt.Sprintf("[queue: %d] %s", qLen, promptPrefix)
	}
	promptStr = queuePrefix + theme.PromptStyle().Render(promptPrefix)
	return promptStr, fullPrefixPlain
}
