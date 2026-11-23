package style

import (
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v3/styles"
)

const maxCustomThemeBytes = 64 << 10

// CustomThemeSpec defines the JSON schema for user-authored theme files stored
// in ~/.loop/themes/<name>.json (or <workspace>/.loop/themes/<name>.json).
// Any omitted color field inherits cleanly from Base (default "studio").
type CustomThemeSpec struct {
	Name           string `json:"name,omitempty"`
	Description    string `json:"description,omitempty"`
	Base           string `json:"base,omitempty"`
	Light          *bool  `json:"light,omitempty"`
	Syntax         string `json:"syntax,omitempty"`
	Primary        string `json:"primary,omitempty"`
	Secondary      string `json:"secondary,omitempty"`
	Highlight      string `json:"highlight,omitempty"`
	Text           string `json:"text,omitempty"`
	TextMuted      string `json:"text_muted,omitempty"`
	Background     string `json:"background,omitempty"`
	Border         string `json:"border,omitempty"`
	BorderActive   string `json:"border_active,omitempty"`
	BorderInactive string `json:"border_inactive,omitempty"`
	Success        string `json:"success,omitempty"`
	Warning        string `json:"warning,omitempty"`
	Error          string `json:"error,omitempty"`
}

type loadedCustomTheme struct {
	name        string
	description string
	light       bool
	path        string
	spec        CustomThemeSpec
	theme       UITheme
}

var (
	customThemesMu   sync.RWMutex
	customThemes     = map[string]loadedCustomTheme{}
	customThemeOrder []string
	customThemeDirs  []string
)

// IsValidThemeName validates a safe lowercase filename-compatible theme name.
func IsValidThemeName(name string) bool {
	name = strings.TrimSpace(name)
	if len(name) == 0 || len(name) > 40 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		isAlphaNum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if i == 0 && !isAlphaNum {
			return false
		}
		if !isAlphaNum && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// NormalizeCustomThemeName lowercases and trims a candidate theme name.
func NormalizeCustomThemeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func resolveCustomThemeSpec(spec CustomThemeSpec, fallbackName string) (loadedCustomTheme, error) {
	name := NormalizeCustomThemeName(spec.Name)
	if name == "" {
		name = NormalizeCustomThemeName(fallbackName)
	}
	if !IsValidThemeName(name) {
		return loadedCustomTheme{}, fmt.Errorf("invalid theme name %q (use lowercase letters, digits, '-' or '_')", name)
	}

	baseName := builtinCanonicalName(spec.Base)
	if baseName == "" {
		baseName = "studio"
	}
	t := builtinTheme(baseName)
	light := builtinIsLight(baseName)
	if spec.Light != nil {
		light = *spec.Light
	}

	applyHex := func(field, raw string, dst *colorVal) error {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil
		}
		norm, ok := NormalizeHexColor(raw)
		if !ok {
			return fmt.Errorf("theme %q field %q has invalid hex color %q (expected #RRGGBB)", name, field, raw)
		}
		*dst = Color(norm)
		return nil
	}

	applyField := func(field, raw string, current *colorVal) error {
		return applyHex(field, raw, current)
	}

	mustColor := func(c color.Color, fallback string) colorVal {
		if cv := toColorVal(c); cv != nil {
			return *cv
		}
		return Color(fallback)
	}

	primary := mustColor(t.Primary, "#8BB8D6")
	secondary := mustColor(t.Secondary, "#9AA8C7")
	highlight := mustColor(t.Highlight, "#D8C38E")
	var textColor color.Color = t.Text
	textMuted := mustColor(t.TextMuted, "#8592A6")
	bg := mustColor(t.Background, "#0F172A")
	border := mustColor(t.Border, "#333C4A")
	borderActive := mustColor(t.BorderActive, "#8BB8D6")
	borderInactive := mustColor(t.BorderInactive, "#333C4A")
	success := mustColor(t.Success, "#7EB89E")
	warning := mustColor(t.Warning, "#D6A66C")
	errColor := mustColor(t.Error, "#D47A86")

	if rawText := strings.TrimSpace(spec.Text); rawText != "" {
		norm, ok := NormalizeHexColor(rawText)
		if !ok {
			return loadedCustomTheme{}, fmt.Errorf("theme %q field %q has invalid hex color %q (expected #RRGGBB)", name, "text", rawText)
		}
		textColor = Color(norm)
	}

	checks := []struct {
		field string
		raw   string
		dst   *colorVal
	}{
		{"primary", spec.Primary, &primary},
		{"secondary", spec.Secondary, &secondary},
		{"highlight", spec.Highlight, &highlight},
		{"text_muted", spec.TextMuted, &textMuted},
		{"background", spec.Background, &bg},
		{"border", spec.Border, &border},
		{"border_active", spec.BorderActive, &borderActive},
		{"border_inactive", spec.BorderInactive, &borderInactive},
		{"success", spec.Success, &success},
		{"warning", spec.Warning, &warning},
		{"error", spec.Error, &errColor},
	}
	for _, c := range checks {
		if err := applyField(c.field, c.raw, c.dst); err != nil {
			return loadedCustomTheme{}, err
		}
	}
	if strings.TrimSpace(spec.Primary) != "" && strings.TrimSpace(spec.BorderActive) == "" {
		borderActive = primary
	}
	if strings.TrimSpace(spec.Border) != "" && strings.TrimSpace(spec.BorderInactive) == "" {
		borderInactive = border
	}

	chromaStyle := t.ChromaStyle
	if syn := strings.TrimSpace(spec.Syntax); syn != "" && !strings.EqualFold(syn, "auto") {
		normSyn := normalizeChromaStyle(syn)
		if _, ok := styles.Lookup(normSyn); !ok {
			return loadedCustomTheme{}, fmt.Errorf("theme %q has unknown syntax style %q", name, syn)
		}
		chromaStyle = normSyn
	}

	desc := strings.TrimSpace(spec.Description)
	if desc == "" {
		desc = "custom user theme"
	}

	resolved := UITheme{
		Primary:        primary,
		Secondary:      secondary,
		Highlight:      highlight,
		Text:           textColor,
		TextMuted:      textMuted,
		Background:     bg,
		Border:         border,
		BorderActive:   borderActive,
		BorderInactive: borderInactive,
		Success:        success,
		Warning:        warning,
		Error:          errColor,
		ChromaStyle:    chromaStyle,
	}

	normSpec := CustomThemeSpec{
		Name:           name,
		Description:    desc,
		Base:           baseName,
		Light:          &light,
		Syntax:         chromaStyle,
		Primary:        primary.Hex(),
		Secondary:      secondary.Hex(),
		Highlight:      highlight.Hex(),
		Text:           ColorHex(textColor),
		TextMuted:      textMuted.Hex(),
		Background:     bg.Hex(),
		Border:         border.Hex(),
		BorderActive:   borderActive.Hex(),
		BorderInactive: borderInactive.Hex(),
		Success:        success.Hex(),
		Warning:        warning.Hex(),
		Error:          errColor.Hex(),
	}

	return loadedCustomTheme{
		name:        name,
		description: desc,
		light:       light,
		spec:        normSpec,
		theme:       resolved,
	}, nil
}

// LoadCustomThemes loads all *.json theme definitions from dirs (later dirs
// take precedence) and remembers dirs for future ReloadCustomThemes calls.
func LoadCustomThemes(dirs ...string) (int, []error) {
	cleaned := make([]string, 0, len(dirs))
	seenDir := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		c := filepath.Clean(d)
		if seenDir[c] {
			continue
		}
		seenDir[c] = true
		cleaned = append(cleaned, c)
	}

	loaded := make(map[string]loadedCustomTheme)
	var errs []error

	for _, dir := range cleaned {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("read themes dir %s: %w", dir, err))
			}
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
				continue
			}
			fullPath := filepath.Join(dir, entry.Name())
			info, err := entry.Info()
			if err != nil {
				errs = append(errs, fmt.Errorf("stat %s: %w", fullPath, err))
				continue
			}
			if info.Size() > maxCustomThemeBytes {
				errs = append(errs, fmt.Errorf("theme file %s exceeds %d bytes", fullPath, maxCustomThemeBytes))
				continue
			}
			raw, err := os.ReadFile(fullPath)
			if err != nil {
				errs = append(errs, fmt.Errorf("read %s: %w", fullPath, err))
				continue
			}
			var spec CustomThemeSpec
			if err := json.Unmarshal(raw, &spec); err != nil {
				errs = append(errs, fmt.Errorf("parse %s: %w", fullPath, err))
				continue
			}
			stem := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			item, err := resolveCustomThemeSpec(spec, stem)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", fullPath, err))
				continue
			}
			item.path = fullPath
			loaded[item.name] = item
		}
	}

	order := make([]string, 0, len(loaded))
	for name := range loaded {
		order = append(order, name)
	}
	sort.Strings(order)

	customThemesMu.Lock()
	customThemes = loaded
	customThemeOrder = order
	customThemeDirs = cleaned
	customThemesMu.Unlock()

	return len(loaded), errs
}

// ReloadCustomThemes re-reads custom theme files from the configured directories.
func ReloadCustomThemes() (int, []error) {
	customThemesMu.RLock()
	dirs := append([]string(nil), customThemeDirs...)
	customThemesMu.RUnlock()
	if len(dirs) == 0 {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			dirs = []string{filepath.Join(home, ".loop", "themes")}
		}
	}
	return LoadCustomThemes(dirs...)
}

// CustomThemeDirs returns the active custom theme search directories.
func CustomThemeDirs() []string {
	customThemesMu.RLock()
	defer customThemesMu.RUnlock()
	return append([]string(nil), customThemeDirs...)
}

// SaveCustomTheme validates and writes spec to <dir>/<name>.json, then reloads custom themes.
func SaveCustomTheme(dir string, spec CustomThemeSpec) (string, error) {
	resolved, err := resolveCustomThemeSpec(spec, spec.Name)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(dir) == "" {
		home, herr := os.UserHomeDir()
		if herr != nil || home == "" {
			return "", fmt.Errorf("could not determine home directory for custom themes")
		}
		dir = filepath.Join(home, ".loop", "themes")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create themes directory: %w", err)
	}
	target := filepath.Join(dir, resolved.name+".json")
	data, err := json.MarshalIndent(resolved.spec, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode theme json: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", target, err)
	}

	customThemesMu.RLock()
	dirs := append([]string(nil), customThemeDirs...)
	customThemesMu.RUnlock()
	hasDir := false
	cleanDir := filepath.Clean(dir)
	for _, d := range dirs {
		if filepath.Clean(d) == cleanDir {
			hasDir = true
			break
		}
	}
	if !hasDir {
		dirs = append([]string{cleanDir}, dirs...)
	}
	_, _ = LoadCustomThemes(dirs...)
	return target, nil
}

// DeleteCustomTheme deletes a saved user theme file and refreshes the registry.
func DeleteCustomTheme(name string) (string, error) {
	norm := NormalizeCustomThemeName(name)
	customThemesMu.RLock()
	item, ok := customThemes[norm]
	customThemesMu.RUnlock()
	if !ok || item.path == "" {
		return "", fmt.Errorf("custom theme %q not found", name)
	}
	if err := os.Remove(item.path); err != nil {
		return "", fmt.Errorf("remove %s: %w", item.path, err)
	}
	_, _ = ReloadCustomThemes()
	return item.path, nil
}

// IsCustomTheme reports whether name is backed by a user theme JSON file.
func IsCustomTheme(name string) bool {
	norm := NormalizeCustomThemeName(name)
	customThemesMu.RLock()
	_, ok := customThemes[norm]
	customThemesMu.RUnlock()
	return ok
}

// CustomThemePath returns the disk path of a loaded custom theme, if any.
func CustomThemePath(name string) (string, bool) {
	norm := NormalizeCustomThemeName(name)
	customThemesMu.RLock()
	item, ok := customThemes[norm]
	customThemesMu.RUnlock()
	if !ok {
		return "", false
	}
	return item.path, true
}

// ThemeSpecFromName builds a complete editable CustomThemeSpec from any existing
// custom or built-in theme (falling back to "studio").
func ThemeSpecFromName(name string) CustomThemeSpec {
	norm := NormalizeCustomThemeName(name)
	customThemesMu.RLock()
	if item, ok := customThemes[norm]; ok {
		spec := item.spec
		customThemesMu.RUnlock()
		return spec
	}
	customThemesMu.RUnlock()

	base := builtinCanonicalName(name)
	if base == "" {
		base = "studio"
	}
	t := builtinTheme(base)
	light := builtinIsLight(base)
	return CustomThemeSpec{
		Name:           base,
		Description:    ThemeDescription(base),
		Base:           base,
		Light:          &light,
		Syntax:         t.ChromaStyle,
		Primary:        ColorHex(t.Primary),
		Secondary:      ColorHex(t.Secondary),
		Highlight:      ColorHex(t.Highlight),
		Text:           ColorHex(t.Text),
		TextMuted:      ColorHex(t.TextMuted),
		Background:     ColorHex(t.Background),
		Border:         ColorHex(t.Border),
		BorderActive:   ColorHex(t.BorderActive),
		BorderInactive: ColorHex(t.BorderInactive),
		Success:        ColorHex(t.Success),
		Warning:        ColorHex(t.Warning),
		Error:          ColorHex(t.Error),
	}
}

// ResolveSpecPreview converts an in-progress CustomThemeSpec into a UITheme for live preview.
func ResolveSpecPreview(spec CustomThemeSpec) (UITheme, error) {
	tmp := spec
	if strings.TrimSpace(tmp.Name) == "" {
		tmp.Name = "preview"
	}
	resolved, err := resolveCustomThemeSpec(tmp, "preview")
	if err != nil {
		return UITheme{}, err
	}
	return resolved.theme, nil
}
