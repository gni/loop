package theme

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v3/quick"

	"loop/pkg/terminal"
	"loop/pkg/ui/interactive"
	"loop/pkg/ui/screen"
	"loop/pkg/ui/style"
)

// PickerAction indicates whether the user chose an existing theme, or asked to
// create or edit a custom theme from the picker.
type PickerAction int

const (
	PickerSelect PickerAction = iota
	PickerCreate
	PickerEdit
)

// PickerResult describes the outcome of RunThemePicker.
type PickerResult struct {
	Action PickerAction
	Name   string
	Base   string
}

// RunThemePicker launches an interactive theme selector with live palette and
// syntax highlighting preview as the user navigates themes.
func RunThemePicker(
	ctx context.Context,
	currentTheme string,
	syntaxTheme string,
	rlInput io.Reader,
	rlOutput io.Writer,
) (PickerResult, error) {
	var result PickerResult
	err := interactive.RunAltScreenSession(
		ctx,
		rlInput,
		rlOutput,
		func(sr *interactive.SessionReader, signals chan os.Signal) error {
			var pickErr error
			result, pickErr = runThemePickerTTY(sr, signals, rlOutput, currentTheme, syntaxTheme)
			return pickErr
		},
		func(sr *interactive.SessionReader) error {
			picked, pickErr := runThemePickerFallback(sr, rlOutput, currentTheme)
			result = PickerResult{Action: PickerSelect, Name: picked}
			return pickErr
		},
	)
	return result, err
}

func runThemePickerTTY(
	sr *interactive.SessionReader,
	signals chan os.Signal,
	output io.Writer,
	currentTheme string,
	syntaxTheme string,
) (PickerResult, error) {
	names := style.GetThemeNames()
	if len(names) == 0 {
		return PickerResult{}, interactive.ErrSelectionCancelled
	}

	activeCanon := style.CanonicalThemeName(currentTheme)
	if activeCanon == "" {
		activeCanon = "studio"
	}

	selected := 0
	for i, name := range names {
		if name == activeCanon {
			selected = i
			break
		}
	}

	query := ""
	scrollOffset := 0
	comp := screen.NewCompositor(80, 24)
	const maxQueryBytes = 128

	for {
		var filtered []string
		lowerQ := strings.ToLower(query)
		for _, name := range names {
			desc := style.ThemeDescription(name)
			era := style.ThemeEra(name)
			eraAlias := era
			if era == "custom" {
				eraAlias = "custom user"
			}
			tone := "dark"
			if style.IsLightTheme(name) {
				tone = "light"
			}
			if lowerQ == "" ||
				strings.Contains(strings.ToLower(name), lowerQ) ||
				strings.Contains(strings.ToLower(desc), lowerQ) ||
				strings.Contains(eraAlias, lowerQ) ||
				strings.Contains(tone, lowerQ) {
				filtered = append(filtered, name)
			}
		}
		if len(filtered) == 0 {
			selected = 0
		} else {
			selected = max(0, min(selected, len(filtered)-1))
		}

		width, height := terminal.GetDimensions()
		if width <= 0 {
			width = 80
		}
		if height <= 0 {
			height = 24
		}
		if width < 16 || height < 8 {
			rows := []string{style.TruncateCells("Resize terminal; Esc exits", max(1, width))}
			if err := interactive.FlushMenuScreen(comp, output, width, height, rows, screen.FlushOptions{}); err != nil {
				return PickerResult{}, err
			}
			key, resized, err := sr.ReadKeyOrResize(signals)
			if err != nil {
				return PickerResult{}, err
			}
			if resized {
				terminal.RefreshDimensions()
				continue
			}
			switch string(key) {
			case "\x1b", "\x03", "\x04":
				return PickerResult{}, interactive.ErrSelectionCancelled
			}
			continue
		}

		previewName := activeCanon
		if len(filtered) > 0 {
			previewName = filtered[selected]
		}
		previewTheme := style.ResolveConfiguredTheme(previewName, syntaxTheme)

		availW := max(1, width-1)
		borderStyle := style.NewStyle().Foreground(previewTheme.Border)
		mutedStyle := style.NewStyle().Foreground(previewTheme.TextMuted)
		titleStyle := style.NewStyle().Foreground(previewTheme.Primary).Bold(true)

		showPreview := height >= 14
		chromeRows := 5
		if showPreview {
			chromeRows = 9
		}
		visible := max(1, height-chromeRows)
		if selected < scrollOffset {
			scrollOffset = selected
		}
		if selected >= scrollOffset+visible {
			scrollOffset = selected - visible + 1
		}
		maxStart := max(0, len(filtered)-visible)
		scrollOffset = min(max(0, scrollOffset), maxStart)
		start := scrollOffset
		end := min(len(filtered), start+visible)

		counter := fmt.Sprintf("0/%d", len(names))
		if len(filtered) > 0 {
			counter = fmt.Sprintf("%d/%d", selected+1, len(filtered))
			if query != "" {
				counter = fmt.Sprintf("%d/%d matches", selected+1, len(filtered))
			}
		}
		titleText := "loop themes · preview: " + previewName
		titleMax := max(1, availW-style.CellWidth(counter)-2)
		heading := style.TruncateCells(titleText, titleMax)
		headerGap := strings.Repeat(" ", max(1, availW-style.CellWidth(heading)-style.CellWidth(counter)))
		headerRow := titleStyle.Render(heading) + headerGap + mutedStyle.Render(counter)

		var searchRow string
		if query == "" {
			searchRow = mutedStyle.Render(style.TruncateCells("  / filter by name, custom, old-school, new-gen, dark, or light", availW))
		} else {
			searchRow = mutedStyle.Render("  / ") + style.NewStyle().Foreground(previewTheme.Text).Render(style.TruncateCells(interactive.MenuDisplayText(query), max(1, availW-4)))
		}
		divider := borderStyle.Render(strings.Repeat("─", availW))

		rows := make([]string, 0, height)
		rows = append(rows, headerRow, searchRow, divider)
		if len(filtered) == 0 {
			rows = append(rows, mutedStyle.Render("  No matching themes"))
		}

		for i := start; i < end; i++ {
			name := filtered[i]
			rowTheme := style.GetTheme(name)
			swatches := formatPaletteDots(rowTheme)
			nameCol := name
			if name == activeCanon {
				nameCol = name + " *"
			}
			eraTag := "new"
			switch style.ThemeEra(name) {
			case "custom":
				eraTag = "user"
			case "old-school":
				eraTag = "old"
			}
			desc := style.ThemeDescription(name)
			labelPlain := fmt.Sprintf("%-20s %-4s %s", nameCol, eraTag, desc)
			maxLabel := max(1, availW-14)
			if style.CellWidth(labelPlain) > maxLabel && maxLabel > 1 {
				labelPlain = style.TruncateCells(labelPlain, maxLabel-1) + "…"
			} else {
				labelPlain = style.TruncateCells(labelPlain, maxLabel)
			}
			if i == selected {
				rows = append(rows, titleStyle.Render("  › ")+swatches+"  "+titleStyle.Render(labelPlain))
			} else {
				rows = append(rows, "    "+swatches+"  "+style.NewStyle().Foreground(previewTheme.Text).Render(labelPlain))
			}
		}

		if showPreview {
			for len(rows) < 3+visible {
				rows = append(rows, "")
			}
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
			synName := previewTheme.ChromaStyle
			if synName == "" {
				synName = "github-dark"
			}
			syntaxRow := fmt.Sprintf("  %s %s",
				mutedStyle.Render("Syntax ("+synName+"):"),
				renderSyntaxPreview(synName),
			)
			rows = append(rows, paletteRow, syntaxRow, "")
		} else {
			rows = append(rows, "")
		}

		rows = append(rows, mutedStyle.Render(style.TruncateCells("  ↑/↓ preview · Enter apply · Ctrl+N new · Ctrl+E edit · Esc cancel", availW)))
		if err := interactive.FlushMenuScreen(comp, output, width, height, rows, screen.FlushOptions{}); err != nil {
			return PickerResult{}, err
		}

		key, resized, err := sr.ReadKeyOrResize(signals)
		if err != nil {
			return PickerResult{}, err
		}
		if resized {
			terminal.RefreshDimensions()
			continue
		}
		if interactive.AppendPastedQuery(key, &query, maxQueryBytes) {
			selected = 0
			continue
		}
		if wheel := interactive.MouseWheelDelta(key); wheel != 0 {
			if len(filtered) > 0 {
				selected = min(len(filtered)-1, max(0, selected+wheel))
			}
			continue
		}
		if _, clickY, ok := interactive.MouseLeftClick(key); ok {
			if len(filtered) > 0 && clickY >= 4 && clickY < 4+(end-start) {
				clicked := start + (clickY - 4)
				if clicked == selected {
					return PickerResult{Action: PickerSelect, Name: filtered[selected]}, nil
				}
				selected = clicked
			}
			continue
		}

		switch string(key) {
		case "\x1b", "\x03", "\x04":
			if query != "" && string(key) == "\x1b" {
				query = ""
				selected = 0
				continue
			}
			return PickerResult{}, interactive.ErrSelectionCancelled
		case "\r", "\n":
			if len(filtered) > 0 {
				return PickerResult{Action: PickerSelect, Name: filtered[selected]}, nil
			}
		case "\x0e": // Ctrl+N: create new custom theme based on highlighted
			return PickerResult{Action: PickerCreate, Base: previewName}, nil
		case "\x05": // Ctrl+E: edit highlighted theme
			return PickerResult{Action: PickerEdit, Name: previewName, Base: previewName}, nil
		case "\x1b[A", "\x1bOA":
			if len(filtered) > 0 {
				selected = (selected - 1 + len(filtered)) % len(filtered)
			}
		case "\x1b[B", "\x1bOB":
			if len(filtered) > 0 {
				selected = (selected + 1) % len(filtered)
			}
		case "\x1b[5~":
			selected = max(0, selected-visible)
		case "\x1b[6~":
			selected = min(max(0, len(filtered)-1), selected+visible)
		case "\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[7~":
			selected = 0
		case "\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~":
			selected = max(0, len(filtered)-1)
		case "\x7f", "\x08":
			if len(query) > 0 {
				_, size := utf8.DecodeLastRuneInString(query)
				query = query[:len(query)-size]
			}
			selected = 0
		default:
			if len(key) > 0 && key[0] != 27 {
				for _, r := range string(key) {
					if r >= 32 && r != 127 && !unicode.IsControl(r) {
						size := utf8.RuneLen(r)
						if size > 0 && len(query)+size <= maxQueryBytes {
							query += string(r)
						}
					}
				}
				selected = 0
			}
		}
	}
}

func runThemePickerFallback(sr *interactive.SessionReader, output io.Writer, currentTheme string) (string, error) {
	names := style.GetThemeNames()
	activeCanon := style.CanonicalThemeName(currentTheme)
	if activeCanon == "" {
		activeCanon = "studio"
	}
	for {
		fmt.Fprint(output, "\r\n==================================================\r\n")
		fmt.Fprint(output, "                   loop themes                    \r\n")
		fmt.Fprint(output, "==================================================\r\n")
		for i, name := range names {
			tag := ""
			if name == activeCanon {
				tag = " [active]"
			}
			fmt.Fprintf(output, "  [%d] %s%s - %s\r\n", i+1, name, tag, style.ThemeDescription(name))
		}
		fmt.Fprint(output, "--------------------------------------------------\r\n")
		fmt.Fprint(output, "choose theme number (or q to cancel): ")
		line, err := interactive.ReadInputRaw(sr, output)
		if err != nil {
			return "", err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "q" || line == "exit" {
			return "", interactive.ErrSelectionCancelled
		}
		if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= len(names) {
			return names[n-1], nil
		}
		if canon := style.CanonicalThemeName(line); canon != "" {
			return canon, nil
		}
		fmt.Fprint(output, "invalid choice. please try again.\r\n")
	}
}

func renderSyntaxPreview(synName string) string {
	var codeBuf bytes.Buffer
	sampleCode := `func main() { fmt.Println("Hello, loop!") }`
	if formatter := style.ChromaFormatterName(); formatter != "" {
		_ = quick.Highlight(&codeBuf, sampleCode, "go", formatter, synName)
	} else {
		codeBuf.WriteString(sampleCode)
	}
	return strings.TrimSpace(codeBuf.String())
}

func formatPaletteDots(t style.UITheme) string {
	return style.NewStyle().Foreground(t.Primary).Render("●") +
		style.NewStyle().Foreground(t.Secondary).Render("●") +
		style.NewStyle().Foreground(t.Highlight).Render("●") +
		style.NewStyle().Foreground(t.Success).Render("●") +
		style.NewStyle().Foreground(t.Warning).Render("●") +
		style.NewStyle().Foreground(t.Error).Render("●")
}
