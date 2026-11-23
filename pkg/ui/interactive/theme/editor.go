package theme

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"loop/pkg/terminal"
	"loop/pkg/ui/interactive"
	"loop/pkg/ui/screen"
	"loop/pkg/ui/style"
)

type editorFieldKind int

const (
	fieldSave editorFieldKind = iota
	fieldText
	fieldBase
	fieldBool
	fieldSyntax
	fieldColor
)

type editorField struct {
	key         string
	label       string
	description string
	kind        editorFieldKind
	get         func(s *style.CustomThemeSpec) string
	set         func(s *style.CustomThemeSpec, val string)
}

func themeEditorFields() []editorField {
	return []editorField{
		{
			key:         "save",
			label:       "save & apply",
			description: "Write ~/.loop/themes/<name>.json and switch to this theme now (shortcut: s)",
			kind:        fieldSave,
			get:         func(s *style.CustomThemeSpec) string { return "write json & activate" },
		},
		{
			key:         "name",
			label:       "name",
			description: "Lowercase theme identifier (letters, digits, '-' or '_'; saved as <name>.json)",
			kind:        fieldText,
			get:         func(s *style.CustomThemeSpec) string { return s.Name },
			set:         func(s *style.CustomThemeSpec, v string) { s.Name = style.NormalizeCustomThemeName(v) },
		},
		{
			key:         "description",
			label:       "description",
			description: "Short caption shown in /theme picker and /theme list",
			kind:        fieldText,
			get:         func(s *style.CustomThemeSpec) string { return s.Description },
			set:         func(s *style.CustomThemeSpec, v string) { s.Description = strings.TrimSpace(v) },
		},
		{
			key:         "base",
			label:       "base palette",
			description: "Built-in palette to inherit from (changing loads its colors and syntax)",
			kind:        fieldBase,
			get:         func(s *style.CustomThemeSpec) string { return s.Base },
		},
		{
			key:         "light",
			label:       "background tone",
			description: "Use light for daylight terminal backgrounds, dark for dark terminals (Space toggles)",
			kind:        fieldBool,
			get: func(s *style.CustomThemeSpec) string {
				if s.Light != nil && *s.Light {
					return "light"
				}
				return "dark"
			},
		},
		{
			key:         "syntax",
			label:       "syntax style",
			description: "Chroma code highlighting theme paired with this palette (Space cycles)",
			kind:        fieldSyntax,
			get:         func(s *style.CustomThemeSpec) string { return s.Syntax },
			set:         func(s *style.CustomThemeSpec, v string) { s.Syntax = v },
		},
		{
			key:         "primary",
			label:       "primary",
			description: "Titles, prompt glyph, focused controls (#RRGGBB; empty resets to base)",
			kind:        fieldColor,
			get:         func(s *style.CustomThemeSpec) string { return s.Primary },
			set: func(s *style.CustomThemeSpec, v string) {
				prev := s.Primary
				s.Primary = v
				if strings.EqualFold(s.BorderActive, prev) || s.BorderActive == "" {
					s.BorderActive = v
				}
			},
		},
		{
			key:         "secondary",
			label:       "secondary",
			description: "Section labels, metadata accents, links (#RRGGBB; empty resets to base)",
			kind:        fieldColor,
			get:         func(s *style.CustomThemeSpec) string { return s.Secondary },
			set:         func(s *style.CustomThemeSpec, v string) { s.Secondary = v },
		},
		{
			key:         "highlight",
			label:       "highlight",
			description: "Search matches, badges, warm emphasis (#RRGGBB; empty resets to base)",
			kind:        fieldColor,
			get:         func(s *style.CustomThemeSpec) string { return s.Highlight },
			set:         func(s *style.CustomThemeSpec, v string) { s.Highlight = v },
		},
		{
			key:         "text",
			label:       "text",
			description: "Primary readable body and prompt text (#RRGGBB; empty resets to base)",
			kind:        fieldColor,
			get:         func(s *style.CustomThemeSpec) string { return s.Text },
			set:         func(s *style.CustomThemeSpec, v string) { s.Text = v },
		},
		{
			key:         "text_muted",
			label:       "text_muted",
			description: "Secondary metadata, timestamps, hints (#RRGGBB; empty resets to base)",
			kind:        fieldColor,
			get:         func(s *style.CustomThemeSpec) string { return s.TextMuted },
			set:         func(s *style.CustomThemeSpec, v string) { s.TextMuted = v },
		},
		{
			key:         "border",
			label:       "border",
			description: "Rules, dividers, subtle chrome lines (#RRGGBB; empty resets to base)",
			kind:        fieldColor,
			get:         func(s *style.CustomThemeSpec) string { return s.Border },
			set: func(s *style.CustomThemeSpec, v string) {
				prev := s.Border
				s.Border = v
				if strings.EqualFold(s.BorderInactive, prev) || s.BorderInactive == "" {
					s.BorderInactive = v
				}
			},
		},
		{
			key:         "border_active",
			label:       "border_active",
			description: "Focused frame and active separator border (#RRGGBB; empty resets to base)",
			kind:        fieldColor,
			get:         func(s *style.CustomThemeSpec) string { return s.BorderActive },
			set:         func(s *style.CustomThemeSpec, v string) { s.BorderActive = v },
		},
		{
			key:         "border_inactive",
			label:       "border_inactive",
			description: "Unfocused frame border (#RRGGBB; empty resets to base)",
			kind:        fieldColor,
			get:         func(s *style.CustomThemeSpec) string { return s.BorderInactive },
			set:         func(s *style.CustomThemeSpec, v string) { s.BorderInactive = v },
		},
		{
			key:         "success",
			label:       "success",
			description: "OK status, diff additions (#RRGGBB; empty resets to base)",
			kind:        fieldColor,
			get:         func(s *style.CustomThemeSpec) string { return s.Success },
			set:         func(s *style.CustomThemeSpec, v string) { s.Success = v },
		},
		{
			key:         "warning",
			label:       "warning",
			description: "Warnings, modified states (#RRGGBB; empty resets to base)",
			kind:        fieldColor,
			get:         func(s *style.CustomThemeSpec) string { return s.Warning },
			set:         func(s *style.CustomThemeSpec, v string) { s.Warning = v },
		},
		{
			key:         "error",
			label:       "error",
			description: "Failures, diff deletions (#RRGGBB; empty resets to base)",
			kind:        fieldColor,
			get:         func(s *style.CustomThemeSpec) string { return s.Error },
			set:         func(s *style.CustomThemeSpec, v string) { s.Error = v },
		},
		{
			key:         "background",
			label:       "background",
			description: "Reference canvas surface color (#RRGGBB; empty resets to base)",
			kind:        fieldColor,
			get:         func(s *style.CustomThemeSpec) string { return s.Background },
			set:         func(s *style.CustomThemeSpec, v string) { s.Background = v },
		},
	}
}

// RunThemeEditor launches an interactive live-preview theme creator/editor.
// It returns the edited spec and saved=true when the user confirms saving.
func RunThemeEditor(
	ctx context.Context,
	initial style.CustomThemeSpec,
	isNew bool,
	rlInput io.Reader,
	rlOutput io.Writer,
) (style.CustomThemeSpec, bool, error) {
	var result style.CustomThemeSpec
	var saved bool
	err := interactive.RunAltScreenSession(
		ctx,
		rlInput,
		rlOutput,
		func(sr *interactive.SessionReader, signals chan os.Signal) error {
			var editErr error
			result, saved, editErr = runThemeEditorTTY(sr, signals, rlOutput, initial, isNew)
			return editErr
		},
		func(_ *interactive.SessionReader) error {
			return fmt.Errorf("interactive terminal required for theme editor")
		},
	)
	return result, saved, err
}

func runThemeEditorTTY(
	sr *interactive.SessionReader,
	signals chan os.Signal,
	output io.Writer,
	initial style.CustomThemeSpec,
	isNew bool,
) (style.CustomThemeSpec, bool, error) {
	spec := initial
	if strings.TrimSpace(spec.Base) == "" {
		spec.Base = "studio"
	}
	if spec.Light == nil {
		light := style.IsLightTheme(spec.Base)
		spec.Light = &light
	}
	// Populate any blank colors from base so every color row shows its active hex.
	fillMissingFromBase(&spec)

	fields := themeEditorFields()
	syntaxChoices := editorSyntaxChoices()
	baseChoices := style.BuiltinThemeNames()

	selected := 0
	if isNew {
		selected = 1 // focus "name" first when creating a new theme
	}
	scrollOffset := 0
	statusMsg := ""
	statusIsErr := false
	comp := screen.NewCompositor(80, 24)

	trySave := func() (bool, string) {
		spec.Name = style.NormalizeCustomThemeName(spec.Name)
		if !style.IsValidThemeName(spec.Name) {
			return false, "enter a valid theme name first (lowercase letters, digits, '-' or '_')"
		}
		if _, err := style.ResolveSpecPreview(spec); err != nil {
			return false, err.Error()
		}
		return true, ""
	}

	applyBase := func(baseName string) {
		keepName := spec.Name
		keepDesc := spec.Description
		spec = style.ThemeSpecFromName(baseName)
		spec.Name = keepName
		spec.Base = baseName
		if strings.TrimSpace(keepDesc) != "" {
			spec.Description = keepDesc
		}
	}

	toggleLight := func() {
		next := true
		if spec.Light != nil {
			next = !*spec.Light
		}
		spec.Light = &next
	}

	for {
		previewTheme, previewErr := style.ResolveSpecPreview(spec)
		if previewErr != nil {
			previewTheme = style.GetTheme(spec.Base)
		}

		width, height := terminal.GetDimensions()
		if width <= 0 {
			width = 80
		}
		if height <= 0 {
			height = 24
		}
		if width < 22 || height < 9 {
			rows := []string{style.TruncateCells("Resize terminal; Esc exits", max(1, width))}
			if err := interactive.FlushMenuScreen(comp, output, width, height, rows, screen.FlushOptions{}); err != nil {
				return spec, false, err
			}
			key, resized, err := sr.ReadKeyOrResize(signals)
			if err != nil {
				return spec, false, err
			}
			if resized {
				terminal.RefreshDimensions()
				continue
			}
			switch string(key) {
			case "\x1b", "\x03", "\x04":
				return spec, false, interactive.ErrSelectionCancelled
			}
			continue
		}

		availW := max(1, width-1)
		borderStyle := style.NewStyle().Foreground(previewTheme.Border)
		mutedStyle := style.NewStyle().Foreground(previewTheme.TextMuted)
		titleStyle := style.NewStyle().Foreground(previewTheme.Primary).Bold(true)
		textStyle := style.NewStyle().Foreground(previewTheme.Text)

		showPreview := height >= 16
		chromeRows := 6 // header + divider + detail + status + spacer + footer
		if showPreview {
			chromeRows = 10 // + divider + palette + chat/diff + syntax
		}
		visible := max(1, height-chromeRows)
		if selected < scrollOffset {
			scrollOffset = selected
		}
		if selected >= scrollOffset+visible {
			scrollOffset = selected - visible + 1
		}
		maxStart := max(0, len(fields)-visible)
		scrollOffset = min(max(0, scrollOffset), maxStart)
		start := scrollOffset
		end := min(len(fields), start+visible)

		modeLabel := "edit theme"
		if isNew {
			modeLabel = "new theme"
		}
		nameBadge := spec.Name
		if nameBadge == "" {
			nameBadge = "<unnamed>"
		}
		counter := fmt.Sprintf("%d/%d", selected+1, len(fields))
		heading := style.TruncateCells("loop "+modeLabel+" · "+nameBadge, max(1, availW-style.CellWidth(counter)-2))
		headerGap := strings.Repeat(" ", max(1, availW-style.CellWidth(heading)-style.CellWidth(counter)))
		headerRow := titleStyle.Render(heading) + headerGap + mutedStyle.Render(counter)
		divider := borderStyle.Render(strings.Repeat("─", availW))

		rows := make([]string, 0, height)
		rows = append(rows, headerRow, divider)

		for i := start; i < end; i++ {
			f := fields[i]
			rawVal := f.get(&spec)
			valDisplay := rawVal
			if f.kind == fieldColor {
				if rawVal == "" {
					if i == selected {
						valDisplay = textStyle.Bold(true).Render("(terminal default)")
					} else {
						valDisplay = mutedStyle.Render("(terminal default)")
					}
				} else {
					swatch := style.NewStyle().Foreground(style.Color(rawVal)).Render("██")
					if i == selected {
						valDisplay = swatch + " " + titleStyle.Render(rawVal)
					} else {
						valDisplay = swatch + " " + textStyle.Render(rawVal)
					}
				}
			} else if f.kind == fieldSave {
				saveStyle := style.NewStyle().Foreground(previewTheme.Success).Bold(true)
				valDisplay = saveStyle.Render("[" + rawVal + "]")
			} else {
				if valDisplay == "" {
					valDisplay = "(empty)"
				}
				valDisplay = style.TruncateCells(valDisplay, max(8, availW-26))
				if i == selected {
					valDisplay = textStyle.Bold(true).Render(valDisplay)
				} else {
					valDisplay = mutedStyle.Render(valDisplay)
				}
			}

			labelPadded := fmt.Sprintf("%-16s", f.label)
			if i == selected {
				labelRender := titleStyle.Render(labelPadded)
				if f.kind == fieldSave {
					labelRender = style.NewStyle().Foreground(previewTheme.Success).Bold(true).Render(labelPadded)
				}
				rows = append(rows, titleStyle.Render("  › ")+labelRender+"  "+valDisplay)
			} else {
				rows = append(rows, "    "+textStyle.Render(labelPadded)+"  "+valDisplay)
			}
		}

		for len(rows) < 2+visible {
			rows = append(rows, "")
		}

		if showPreview {
			rows = append(rows, divider)
			paletteRow := fmt.Sprintf("  %s %s %s %s %s %s %s",
				mutedStyle.Render("Palette:"),
				style.NewStyle().Foreground(previewTheme.Primary).Bold(true).Render("Primary"),
				style.NewStyle().Foreground(previewTheme.Secondary).Render("Secondary"),
				style.NewStyle().Foreground(previewTheme.Highlight).Render("Highlight"),
				style.NewStyle().Foreground(previewTheme.Success).Render("Success"),
				style.NewStyle().Foreground(previewTheme.Warning).Render("Warning"),
				style.NewStyle().Foreground(previewTheme.Error).Render("Error"),
			)
			sampleRow := fmt.Sprintf("  %s %s %s %s %s %s",
				style.NewStyle().Foreground(previewTheme.Primary).Bold(true).Render("▹ user"),
				textStyle.Render("chat text"),
				mutedStyle.Render("muted"),
				style.NewStyle().Foreground(previewTheme.Success).Render("+added"),
				style.NewStyle().Foreground(previewTheme.Warning).Render("~changed"),
				style.NewStyle().Foreground(previewTheme.Error).Render("-removed"),
			)
			synName := previewTheme.ChromaStyle
			if synName == "" {
				synName = "github-dark"
			}
			syntaxRow := fmt.Sprintf("  %s %s",
				mutedStyle.Render("Syntax ("+synName+"):"),
				renderSyntaxPreview(synName),
			)
			rows = append(rows, paletteRow, sampleRow, syntaxRow)
		}

		desc := style.TruncateCells("  "+fields[selected].description, availW)
		rows = append(rows, mutedStyle.Render(desc))

		if statusMsg != "" {
			stStyle := style.NewStyle().Foreground(previewTheme.Success)
			if statusIsErr {
				stStyle = style.NewStyle().Foreground(previewTheme.Error).Bold(true)
			}
			rows = append(rows, stStyle.Render(style.TruncateCells("  "+statusMsg, availW)))
		} else {
			rows = append(rows, "")
		}

		rows = append(rows, mutedStyle.Render(style.TruncateCells("  ↑/↓ move · Enter edit · Space toggle · s save & apply · Esc cancel", availW)))

		if err := interactive.FlushMenuScreen(comp, output, width, height, rows, screen.FlushOptions{}); err != nil {
			return spec, false, err
		}

		key, resized, err := sr.ReadKeyOrResize(signals)
		if err != nil {
			return spec, false, err
		}
		if resized {
			terminal.RefreshDimensions()
			continue
		}

		statusMsg = ""
		statusIsErr = false

		if wheel := interactive.MouseWheelDelta(key); wheel != 0 {
			selected = min(len(fields)-1, max(0, selected+wheel))
			continue
		}

		switch string(key) {
		case "\x1b", "\x03", "\x04":
			return spec, false, interactive.ErrSelectionCancelled
		case "s", "S", "\x13":
			if ok, msg := trySave(); ok {
				return spec, true, nil
			} else {
				statusMsg = msg
				statusIsErr = true
			}
		case "\x1b[A", "\x1bOA", "k":
			selected = (selected - 1 + len(fields)) % len(fields)
		case "\x1b[B", "\x1bOB", "j":
			selected = (selected + 1) % len(fields)
		case "\x1b[5~":
			selected = max(0, selected-visible)
		case "\x1b[6~":
			selected = min(len(fields)-1, selected+visible)
		case "\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[7~":
			selected = 0
		case "\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~":
			selected = len(fields) - 1
		case " ":
			f := fields[selected]
			switch f.kind {
			case fieldBool:
				toggleLight()
			case fieldBase:
				nextBase := cycleStringOption(baseChoices, spec.Base)
				applyBase(nextBase)
				statusMsg = "loaded colors from base " + nextBase
			case fieldSyntax:
				spec.Syntax = cycleStringOption(syntaxChoices, spec.Syntax)
			}
		case "\r", "\n":
			f := fields[selected]
			switch f.kind {
			case fieldSave:
				if ok, msg := trySave(); ok {
					return spec, true, nil
				} else {
					statusMsg = msg
					statusIsErr = true
				}
			case fieldBool:
				toggleLight()
			case fieldBase:
				idx, selErr := interactive.RunInteractiveSelect(sr, signals, output, "choose base built-in palette", baseChoices, previewTheme)
				comp.Invalidate()
				if selErr == nil && idx >= 0 && idx < len(baseChoices) {
					applyBase(baseChoices[idx])
					statusMsg = "loaded colors from base " + baseChoices[idx]
				} else if selErr != nil && !errors.Is(selErr, interactive.ErrSelectionCancelled) {
					return spec, false, selErr
				}
			case fieldSyntax:
				idx, selErr := interactive.RunInteractiveSelect(sr, signals, output, "choose Chroma syntax style", syntaxChoices, previewTheme)
				comp.Invalidate()
				if selErr == nil && idx >= 0 && idx < len(syntaxChoices) {
					spec.Syntax = syntaxChoices[idx]
				} else if selErr != nil && !errors.Is(selErr, interactive.ErrSelectionCancelled) {
					return spec, false, selErr
				}
			case fieldText:
				val, editErr := interactive.ReadMenuField(
					sr, signals, output, previewTheme,
					"Edit "+f.label, f.description, f.label, f.get(&spec),
					func(candidate string) error {
						if f.key == "name" {
							norm := style.NormalizeCustomThemeName(candidate)
							if !style.IsValidThemeName(norm) {
								return fmt.Errorf("use lowercase letters, digits, '-' or '_' (max 40 chars)")
							}
						}
						return nil
					},
				)
				comp.Invalidate()
				if editErr == nil {
					f.set(&spec, val)
				} else if !errors.Is(editErr, interactive.ErrSelectionCancelled) {
					return spec, false, editErr
				}
			case fieldColor:
				val, editErr := interactive.ReadMenuField(
					sr, signals, output, previewTheme,
					"Edit "+f.label, f.description, "Hex (#RRGGBB)", f.get(&spec),
					func(candidate string) error {
						candidate = strings.TrimSpace(candidate)
						if candidate == "" {
							return nil
						}
						if _, ok := style.NormalizeHexColor(candidate); !ok {
							return fmt.Errorf("invalid hex color %q (expected #RRGGBB or #RGB)", candidate)
						}
						return nil
					},
				)
				comp.Invalidate()
				if editErr == nil {
					val = strings.TrimSpace(val)
					if val == "" {
						baseSpec := style.ThemeSpecFromName(spec.Base)
						f.set(&spec, f.get(&baseSpec))
						statusMsg = "reset " + f.label + " to base " + spec.Base
					} else if norm, ok := style.NormalizeHexColor(val); ok {
						f.set(&spec, norm)
					}
				} else if !errors.Is(editErr, interactive.ErrSelectionCancelled) {
					return spec, false, editErr
				}
			}
		}
	}
}

func fillMissingFromBase(spec *style.CustomThemeSpec) {
	baseSpec := style.ThemeSpecFromName(spec.Base)
	if spec.Syntax == "" {
		spec.Syntax = baseSpec.Syntax
	}
	if spec.Primary == "" {
		spec.Primary = baseSpec.Primary
	}
	if spec.Secondary == "" {
		spec.Secondary = baseSpec.Secondary
	}
	if spec.Highlight == "" {
		spec.Highlight = baseSpec.Highlight
	}
	if spec.Text == "" {
		spec.Text = baseSpec.Text
	}
	if spec.TextMuted == "" {
		spec.TextMuted = baseSpec.TextMuted
	}
	if spec.Background == "" {
		spec.Background = baseSpec.Background
	}
	if spec.Border == "" {
		spec.Border = baseSpec.Border
	}
	if spec.BorderActive == "" {
		spec.BorderActive = baseSpec.BorderActive
	}
	if spec.BorderInactive == "" {
		spec.BorderInactive = baseSpec.BorderInactive
	}
	if spec.Success == "" {
		spec.Success = baseSpec.Success
	}
	if spec.Warning == "" {
		spec.Warning = baseSpec.Warning
	}
	if spec.Error == "" {
		spec.Error = baseSpec.Error
	}
}

func editorSyntaxChoices() []string {
	all := style.GetSyntaxThemeNames()
	out := make([]string, 0, len(all))
	for _, s := range all {
		if strings.EqualFold(s, "auto") {
			continue
		}
		out = append(out, s)
	}
	return out
}

func cycleStringOption(options []string, current string) string {
	if len(options) == 0 {
		return current
	}
	idx := -1
	for i, opt := range options {
		if strings.EqualFold(opt, current) {
			idx = i
			break
		}
	}
	return options[(idx+1)%len(options)]
}
