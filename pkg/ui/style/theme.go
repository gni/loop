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
	var textColor color.Color
	if text != "" {
		textColor = Color(text)
	}
	return UITheme{
		Primary:        Color(primary),
		Secondary:      Color(secondary),
		Highlight:      Color(highlight),
		Text:           textColor,
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
	norm := NormalizeCustomThemeName(themeName)
	if norm != "" {
		customThemesMu.RLock()
		if item, ok := customThemes[norm]; ok {
			customThemesMu.RUnlock()
			return item.theme
		}
		customThemesMu.RUnlock()
	}
	return builtinTheme(themeName)
}

func builtinTheme(themeName string) UITheme {
	switch builtinCanonicalName(themeName) {
	// --- New Generation: refined modern, editorial, low-fatigue palettes ---
	case "ashen":
		return makeTheme(
			"#C4846C", "#A89C94", "#D4B483",
			"#D5D2CE", "#847F7B", "#121212",
			"#32302F", "#C4846C", "",
			"#8AA382", "#C99D66", "#B85C5C",
			"ashen",
		)
	case "graphite":
		return makeTheme(
			"#B4C0D0", "#8E9AA8", "#D2C39A",
			"#D4D7DC", "#7C838E", "#141518",
			"#2E3138", "#B4C0D0", "",
			"#7CA892", "#C7A36B", "#C7767E",
			"ashen",
		)
	case "kanagawa-dragon":
		return makeTheme(
			"#8BA4B0", "#8EA4A2", "#C4B28A",
			"#C5C9C5", "#7A8382", "#181616",
			"#393836", "#8BA4B0", "",
			"#87A987", "#C49A6C", "#C4746E",
			"kanagawa-dragon",
		)
	case "kanagawa":
		return makeTheme(
			"#7E9CD8", "#7AA89F", "#D2B886",
			"#D5D0B6", "#8A897E", "#1F1F28",
			"#3B3B4C", "#7E9CD8", "",
			"#93B070", "#D4986A", "#CF6F79",
			"kanagawa-wave",
		)
	case "everforest":
		return makeTheme(
			"#83B892", "#7FB0AA", "#D2B682",
			"#D0C4A8", "#939F95", "#272E33",
			"#474F54", "#83B892", "",
			"#9EB87E", "#D49573", "#CF7C7E",
			"evergarden",
		)
	case "rose-pine":
		return makeTheme(
			"#95C2CC", "#B59ED4", "#DFB780",
			"#D8D6EC", "#8B87A4", "#232136",
			"#423F56", "#95C2CC", "",
			"#84B8A0", "#DFB780", "#D4728C",
			"rose-pine-moon",
		)
	case "catppuccin-frappe":
		return makeTheme(
			"#8CAAEE", "#99B3D6", "#DEC48E",
			"#C6D0F5", "#8C95B0", "#303446",
			"#4C5166", "#8CAAEE", "",
			"#9BC28D", "#DBA682", "#D8818D",
			"catppuccin-frappe",
		)
	case "catppuccin-mocha":
		return makeTheme(
			"#89AEEC", "#A6B1E6", "#E3CEA0",
			"#C9D2EE", "#8D93AB", "#1E1E2E",
			"#444658", "#89AEEC", "",
			"#9AC996", "#DFA47E", "#DC839C",
			"catppuccin-mocha",
		)
	case "tokyo-night":
		return makeTheme(
			"#7B9CE0", "#7DB4CC", "#CEAC74",
			"#BCC5E8", "#7681A8", "#1A1B26",
			"#39405C", "#7B9CE0", "",
			"#92BA70", "#CEAC74", "#D8768B",
			"tokyonight-moon",
		)
	case "nord":
		return makeTheme(
			"#88B8C8", "#819EC0", "#D9BE8A",
			"#DCE1E8", "#8492A8", "#2E3440",
			"#4A5466", "#88B8C8", "",
			"#9BB488", "#C88A72", "#B8666E",
			"nordic",
		)
	case "minimal":
		return makeTheme(
			"#D8DCE2", "#A4ACB8", "#E6E8EC",
			"#CFD4DC", "#7E8590", "#111317",
			"#343942", "#C4CCD6", "",
			"#82AE94", "#C9AB78", "#C7787F",
			"ashen",
		)
	case "light":
		return makeTheme(
			"#357EA8", "#6B68B8", "#B87A28",
			"", "#758294", "#FAFAFA",
			"#8E9AAB", "#357EA8", "",
			"#2E8A57", "#B87A28", "#C7445C",
			"catppuccin-latte",
		)

	// --- Old School: heritage Unix, CRT phosphor, classic Vim/Emacs & paper ---
	case "zenburn":
		return makeTheme(
			"#88C2C5", "#8FAFD6", "#DFCFA4",
			"#D4D4C6", "#96968A", "#2B2B2B",
			"#4B4B4B", "#88C2C5", "",
			"#7F9F7F", "#D2A78A", "#C28A8A",
			"evergarden",
		)
	case "gruvbox":
		return makeTheme(
			"#83A598", "#8CB57B", "#D7A842",
			"#E2D3AD", "#9E907C", "#282828",
			"#4E4743", "#83A598", "",
			"#A8AE3A", "#D6803A", "#CC5B4E",
			"gruvbox",
		)
	case "solarized-dark":
		return makeTheme(
			"#5697C4", "#5FA5A0", "#B8933B",
			"#9AA8A9", "#6C8086", "#002B36",
			"#204B56", "#5697C4", "",
			"#859936", "#C46C34", "#C8504E",
			"solarized-dark",
		)
	case "amber-crt":
		return makeTheme(
			"#D89E4C", "#B88648", "#E6B86E",
			"#D8B782", "#8C724E", "#14100A",
			"#3E3222", "#D89E4C", "",
			"#A8A458", "#D89E4C", "#C46A52",
			"gruvbox",
		)
	case "green-crt":
		return makeTheme(
			"#7EC48B", "#69A37B", "#B4CFA0",
			"#B5CEB8", "#647D6A", "#0D140F",
			"#283B2E", "#7EC48B", "",
			"#7EC48B", "#B8AA66", "#BA736E",
			"evergarden",
		)
	case "darcula":
		return makeTheme(
			"#6E9BC8", "#9A82B0", "#C9AE6C",
			"#C2C7CC", "#7D848B", "#2B2B2B",
			"#46494D", "#6E9BC8", "",
			"#7BA46B", "#C9955A", "#C46C6A",
			"darcula",
		)
	case "modus":
		return makeTheme(
			"#7CA8D8", "#7DB8B8", "#CFC07A",
			"#DCDCDC", "#8E9298", "#0F1012",
			"#3A3D44", "#7CA8D8", "",
			"#76B882", "#D0A264", "#D47676",
			"modus-vivendi",
		)
	case "paper":
		return makeTheme(
			"#457EA0", "#766CA6", "#AE7A30",
			"", "#807A6E", "#F7F4EC",
			"#9C9588", "#457EA0", "",
			"#43885C", "#B57C32", "#C25259",
			"kanagawa-lotus",
		)
	case "solarized-light":
		return makeTheme(
			"#268BD2", "#2AA198", "#B58900",
			"", "#788B91", "#FDF6E3",
			"#93A1A1", "#268BD2", "",
			"#859900", "#CB4B16", "#DC322F",
			"solarized-light",
		)
	case "studio":
		fallthrough
	default:
		return makeTheme(
			"#8BB8D6", "#9AA8C7", "#D8C38E",
			"#DCE2EA", "#8592A6", "#0F172A",
			"#333C4A", "#8BB8D6", "",
			"#7EB89E", "#D6A66C", "#D47A86",
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
		return "tokyonight-moon"
	case "catppuccin":
		return "catppuccin-mocha"
	case "rosepine":
		return "rose-pine-moon"
	case "modus":
		return "modus-vivendi"
	case "paper":
		return "kanagawa-lotus"
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

// BuiltinThemeNames returns all built-in UI theme identifiers ordered cleanly
// into new-generation and old-school classics.
func BuiltinThemeNames() []string {
	return []string{
		// New generation
		"studio",
		"ashen",
		"graphite",
		"kanagawa-dragon",
		"kanagawa",
		"everforest",
		"rose-pine",
		"catppuccin-frappe",
		"catppuccin-mocha",
		"tokyo-night",
		"nord",
		"minimal",
		"light",
		// Old school
		"zenburn",
		"gruvbox",
		"solarized-dark",
		"amber-crt",
		"green-crt",
		"darcula",
		"modus",
		"paper",
		"solarized-light",
	}
}

// GetThemeNames returns all supported UI theme identifiers ordered cleanly
// into custom user themes, new-generation, and old-school classics.
func GetThemeNames() []string {
	builtins := BuiltinThemeNames()
	customThemesMu.RLock()
	customOrder := append([]string(nil), customThemeOrder...)
	customThemesMu.RUnlock()
	if len(customOrder) == 0 {
		return builtins
	}
	seen := make(map[string]bool, len(customOrder)+len(builtins))
	out := make([]string, 0, len(customOrder)+len(builtins))
	for _, c := range customOrder {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	for _, b := range builtins {
		if !seen[b] {
			seen[b] = true
			out = append(out, b)
		}
	}
	return out
}

// CanonicalThemeName maps any recognized custom or built-in theme name/alias
// to its canonical identifier. Returns "" for unknown names.
func CanonicalThemeName(name string) string {
	norm := NormalizeCustomThemeName(name)
	if norm != "" {
		customThemesMu.RLock()
		if _, ok := customThemes[norm]; ok {
			customThemesMu.RUnlock()
			return norm
		}
		customThemesMu.RUnlock()
	}
	return builtinCanonicalName(name)
}

func builtinCanonicalName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "default", "dark", "studio":
		return "studio"
	case "ashen", "ember":
		return "ashen"
	case "graphite", "zinc", "slate":
		return "graphite"
	case "kanagawa-dragon", "dragon":
		return "kanagawa-dragon"
	case "kanagawa", "kanagawa-wave":
		return "kanagawa"
	case "everforest":
		return "everforest"
	case "rose-pine", "rose-pine-moon", "rosepine":
		return "rose-pine"
	case "catppuccin-frappe", "frappe":
		return "catppuccin-frappe"
	case "catppuccin", "catppuccin-mocha", "mocha":
		return "catppuccin-mocha"
	case "tokyonight", "tokyo-night", "neon":
		return "tokyo-night"
	case "nord", "nord-calm", "nordic":
		return "nord"
	case "minimal", "mono", "plain":
		return "minimal"
	case "light", "alabaster":
		return "light"
	case "zenburn", "earth-calm", "earth":
		return "zenburn"
	case "gruvbox":
		return "gruvbox"
	case "solarized", "solarized-dark":
		return "solarized-dark"
	case "amber", "amber-crt", "crt-amber":
		return "amber-crt"
	case "green", "green-crt", "crt-green", "phosphor", "vt220":
		return "green-crt"
	case "darcula", "jetbrains":
		return "darcula"
	case "modus", "modus-vivendi", "emacs":
		return "modus"
	case "paper", "parchment", "sepia":
		return "paper"
	case "solarized-light":
		return "solarized-light"
	default:
		return ""
	}
}

// ThemeEra classifies a theme as "custom", "new-gen", or "old-school".
func ThemeEra(name string) string {
	norm := NormalizeCustomThemeName(name)
	customThemesMu.RLock()
	if _, ok := customThemes[norm]; ok {
		customThemesMu.RUnlock()
		return "custom"
	}
	customThemesMu.RUnlock()
	switch builtinCanonicalName(name) {
	case "zenburn", "gruvbox", "solarized-dark", "amber-crt", "green-crt", "darcula", "modus", "paper", "solarized-light":
		return "old-school"
	default:
		return "new-gen"
	}
}

// IsLightTheme reports whether a theme targets a light terminal background.
func IsLightTheme(name string) bool {
	norm := NormalizeCustomThemeName(name)
	customThemesMu.RLock()
	if item, ok := customThemes[norm]; ok {
		customThemesMu.RUnlock()
		return item.light
	}
	customThemesMu.RUnlock()
	return builtinIsLight(name)
}

func builtinIsLight(name string) bool {
	switch builtinCanonicalName(name) {
	case "light", "paper", "solarized-light":
		return true
	default:
		return false
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

// ThemeDescription returns a brief visual description for a custom or built-in theme.
func ThemeDescription(name string) string {
	norm := NormalizeCustomThemeName(name)
	customThemesMu.RLock()
	if item, ok := customThemes[norm]; ok {
		customThemesMu.RUnlock()
		return item.description
	}
	customThemesMu.RUnlock()
	switch builtinCanonicalName(name) {
	case "studio":
		return "crisp architectural slate"
	case "ashen":
		return "muted ember & charcoal"
	case "graphite":
		return "editorial zinc & silver"
	case "kanagawa-dragon":
		return "sumi ink & muted jade"
	case "kanagawa":
		return "ukiyo-e wave & parchment"
	case "everforest":
		return "soft moss & cedar bark"
	case "rose-pine":
		return "dusky pine & muted plum"
	case "catppuccin-frappe":
		return "subdued slate pastel"
	case "catppuccin-mocha":
		return "velvety dark pastel"
	case "tokyo-night":
		return "muted indigo twilight"
	case "nord":
		return "arctic frost & polar slate"
	case "minimal":
		return "quiet monochrome ink"
	case "light":
		return "crisp alabaster daylight"
	case "zenburn":
		return "classic low-contrast vim charcoal"
	case "gruvbox":
		return "retro warm ochre & umber"
	case "solarized-dark":
		return "classic precision deep teal"
	case "amber-crt":
		return "1980s P3 amber phosphor"
	case "green-crt":
		return "VT220 sage phosphor"
	case "darcula":
		return "classic muted steel IDE"
	case "modus":
		return "Emacs high-legibility dark"
	case "paper":
		return "warm editorial parchment"
	case "solarized-light":
		return "classic warm sand daylight"
	default:
		return "custom palette"
	}
}

// GetSyntaxThemeNames returns standard supported chroma syntax highlighting styles.
func GetSyntaxThemeNames() []string {
	return []string{
		"auto",
		"github-dark",
		"ashen",
		"kanagawa-dragon",
		"kanagawa-wave",
		"evergarden",
		"rose-pine-moon",
		"catppuccin-frappe",
		"catppuccin-mocha",
		"tokyonight-moon",
		"tokyonight-night",
		"nordic",
		"nord",
		"gruvbox",
		"solarized-dark",
		"darcula",
		"modus-vivendi",
		"onedark",
		"kanagawa-lotus",
		"modus-operandi",
		"catppuccin-latte",
		"solarized-light",
		"github",
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
