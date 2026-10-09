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
	switch strings.ToLower(themeName) {
	case "catppuccin", "catppuccin-mocha":
		return makeTheme(
			"#89B4FA", "#CBA6F7", "#FAB387",
			"#CDD6F4", "#6C7086", "#1E1E2E",
			"#313244", "#89B4FA", "",
			"#A6E3A1", "#F9E2AF", "#F38BA8",
			"catppuccin-mocha",
		)
	case "tokyonight", "tokyo-night", "neon":
		return makeTheme(
			"#7AA2F7", "#BB9AF7", "#7DCFFF",
			"#C0CAF5", "#565F89", "#1A1B26",
			"#292E42", "#7AA2F7", "",
			"#9ECE6A", "#E0AF68", "#F7768E",
			"tokyonight-night",
		)
	case "everforest":
		return makeTheme(
			"#83C092", "#E69875", "#DBBC7F",
			"#D3C6AA", "#859289", "#272E33",
			"#3D484D", "#A7C080", "",
			"#A7C080", "#DBBC7F", "#E67E80",
			"everforest",
		)
	case "gruvbox":
		return makeTheme(
			"#8EC07C", "#D3869B", "#FE8019",
			"#EBDBB2", "#928374", "#282828",
			"#504945", "#FABD2F", "",
			"#B8BB26", "#FABD2F", "#FB4934",
			"gruvbox",
		)
	case "mono", "plain", "minimal":
		return makeTheme(
			"#D4D4D4", "#9E9E9E", "#FFFFFF",
			"#E0E0E0", "#666666", "#121212",
			"#2E2E2E", "#A0A0A0", "",
			"#87AF87", "#D7AF87", "#D75F5F",
			"bw",
		)
	case "light":
		return makeTheme(
			"#268BD2", "#D33682", "#B58900",
			"#475B62", "#93A1A1", "#FDF6E3",
			"#93A1A1", "#268BD2", "#EEE8D5",
			"#859900", "#B58900", "#DC322F",
			"solarized-light",
		)
	case "kanagawa", "kanagawa-wave":
		return makeTheme(
			"#7E9CD8", "#957FB8", "#DCA561",
			"#DCD7BA", "#727169", "#1F1F28",
			"#2A2A37", "#7E9CD8", "",
			"#76946A", "#E6C384", "#C34043",
			"dracula",
		)
	case "rose-pine", "rose-pine-moon", "rosepine":
		return makeTheme(
			"#9CCFD8", "#C4A7E7", "#F6C177",
			"#E0DEF4", "#6E6A86", "#232136",
			"#393552", "#9CCFD8", "",
			"#3E8FB0", "#F6C177", "#EB6F92",
			"dracula",
		)
	case "zenburn", "earth-calm", "earth":
		return makeTheme(
			"#8CD0D3", "#DC8CC3", "#DFAF8F",
			"#DCDCCC", "#7F9F7F", "#2B2B2B",
			"#3F3F3F", "#8CD0D3", "",
			"#7F9F7F", "#DFAF8F", "#CC9393",
			"friendly",
		)
	case "dark", "nord", "nord-calm":
		fallthrough
	default:
		return makeTheme(
			"#88C0D0", "#81A1C1", "#8FBCBB",
			"#D8DEE9", "#616E88", "#2E3440",
			"#3B4252", "#88C0D0", "",
			"#A3BE8C", "#EBCB8B", "#BF616A",
			"nord",
		)
	}
}

// ResolveConfiguredTheme returns the UITheme configured by theme name and syntax highlighting theme.
func ResolveConfiguredTheme(themeName, syntaxTheme string) UITheme {
	theme := GetTheme(themeName)
	if syntaxTheme != "" && syntaxTheme != "auto" {
		theme.ChromaStyle = syntaxTheme
	}
	return theme
}

// GetThemeNames returns all supported UI theme identifiers.
func GetThemeNames() []string {
	return []string{
		"catppuccin-mocha",
		"tokyo-night",
		"everforest",
		"gruvbox",
		"minimal",
		"light",
		"kanagawa",
		"rose-pine",
		"zenburn",
		"nord",
	}
}

// GetSyntaxThemeNames returns standard supported chroma syntax highlighting styles.
func GetSyntaxThemeNames() []string {
	return []string{
		"catppuccin-mocha",
		"tokyonight-night",
		"everforest",
		"gruvbox",
		"bw",
		"solarized-light",
		"dracula",
		"friendly",
		"nord",
		"monokai",
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

