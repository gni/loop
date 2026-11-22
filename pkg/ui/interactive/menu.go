package interactive

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"

	"loop/pkg/config"
	"loop/pkg/terminal"
	"loop/pkg/ui/screen"
	"loop/pkg/ui/style"
)

// Check short writes as well as explicit terminal errors before waiting on input.
func writeMenuFrame(w io.Writer, frame string) error {
	n, err := io.WriteString(w, frame)
	if err == nil && n != len(frame) {
		return io.ErrShortWrite
	}
	return err
}

func ResolveTerminalFD(rlInput io.Reader, rlOutput io.Writer) int {
	if f, ok := rlInput.(*os.File); ok && f != nil {
		return int(f.Fd())
	}
	if f, ok := rlOutput.(*os.File); ok && f != nil {
		return int(f.Fd())
	}
	return int(os.Stdin.Fd())
}

func FlushMenuScreen(comp *screen.Compositor, w io.Writer, width, height int, rows []string, opts screen.FlushOptions) error {
	width = max(1, width)
	height = max(1, height)
	if comp.Width() != width || comp.Height() != height {
		comp.Resize(width, height)
	}
	comp.Clear()
	limit := min(len(rows), height)
	for y := range limit {
		comp.SetRowANSI(y, rows[y])
	}
	return comp.Flush(w, opts)
}

type SettingItem struct {
	SearchOnly  bool
	ID          string
	Name        string
	Value       func() string
	Description string
	Options     []string
	IsBool      bool
	OnToggle    func()
	OnAction    func() error
	OnEdit      func(newVal string) error
}

// Boolean settings go through OnEdit so file, command and menu rules agree.
func (item *SettingItem) toggle() error {
	if item.OnAction != nil {
		return item.OnAction()
	}
	if item.IsBool && item.OnEdit != nil {
		value, err := config.ParseBoolValue(item.Value())
		if err != nil {
			return err
		}
		return item.OnEdit(strconv.FormatBool(!value))
	}
	if item.OnToggle != nil {
		item.OnToggle()
	}
	return nil
}

func WithAltScreenTerminal(rlInput io.Reader, rlOutput io.Writer, fn func() error) (bool, error) {
	fd := ResolveTerminalFD(rlInput, rlOutput)
	if !term.IsTerminal(fd) {
		return false, nil
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return true, err
	}
	defer term.Restore(fd, oldState)

	SetInInteractiveMenu(true)
	defer SetInInteractiveMenu(false)

	terminal.Default().EnterAltScreen(rlOutput)
	defer terminal.Default().ExitAltScreen(rlOutput)
	restorePaste := terminal.Default().BeginBracketedPaste(rlOutput)
	defer restorePaste()

	terminal.RefreshDimensions()
	return true, fn()
}

func GetInteractiveIO(kiReader io.Reader) (io.Reader, io.Writer, func()) {
	var input io.Reader = kiReader
	var inputCloser io.Closer
	if IsNilReader(input) {
		input = os.Stdin
		if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
			input = tty
			inputCloser = tty
		}
	}

	var output io.Writer = os.Stdout
	var outputCloser io.Closer
	if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		output = tty
		outputCloser = tty
	}

	cleanup := func() {
		if inputCloser != nil {
			_ = inputCloser.Close()
		}
		if outputCloser != nil && outputCloser != inputCloser {
			_ = outputCloser.Close()
		}
	}

	return input, output, cleanup
}

func FormatMenuCell(item *SettingItem, selected bool, colWidth int, theme style.UITheme) string {
	nameStr := item.Name
	valStr := item.Value()
	cleanVal := style.StripAnsi(valStr)
	hasAnsiVal := cleanVal != valStr

	keyColWidth := 24
	if colWidth > 0 {
		keyColWidth = min(max(colWidth-16, 14), 26)
	}

	if style.CellWidth(nameStr) > keyColWidth-1 {
		nameStr = style.TruncateCells(nameStr, keyColWidth-1)
	}

	if colWidth > 0 && !hasAnsiVal {
		maxValCells := max(colWidth-keyColWidth-6, 6)
		if style.CellWidth(valStr) > maxValCells {
			valStr = style.TruncateCells(valStr, maxValCells)
		}
	}

	nameWidth := style.CellWidth(nameStr)
	leader := ""
	if nameWidth < keyColWidth {
		leader = strings.Repeat("·", keyColWidth-nameWidth)
	}
	leaderStyled := style.NewStyle().Foreground(theme.Border).Render(leader)

	if selected {
		marker := style.NewStyle().Foreground(theme.Primary).Bold(true).Render("›")
		nameStyled := style.NewStyle().Foreground(theme.Primary).Bold(true).Render(nameStr)
		valStyled := valStr
		if !hasAnsiVal {
			valStyled = style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(valStr)
		}
		bracket := style.NewStyle().Foreground(theme.Secondary)

		valStrFormatted := ""
		if valStr != "" {
			valStrFormatted = fmt.Sprintf("%s%s%s", bracket.Render("["), valStyled, bracket.Render("]"))
		}
		return fmt.Sprintf("%s %s %s %s", marker, nameStyled, leaderStyled, valStrFormatted)
	}

	nameStyled := style.NewStyle().Foreground(theme.Text).Render(nameStr)
	valStyled := valStr
	if !hasAnsiVal {
		valStyled = style.NewStyle().Foreground(theme.Secondary).Render(valStr)
	}
	return fmt.Sprintf("  %s %s %s", nameStyled, leaderStyled, valStyled)
}

func PadAnsiCell(cell string, width int) string {
	vis := style.CellWidth(cell)
	if vis >= width {
		return cell
	}
	return cell + strings.Repeat(" ", width-vis)
}

func RunSettingsMenuLoop(rlInput io.Reader, rlOutput io.Writer, theme style.UITheme, title string, itemsProvider func() []*SettingItem, extraRender func(buf *strings.Builder)) error {
	searchQuery := ""
	selectedIdx := 0

	sr, closeSR := modalSessionReader(rlInput)
	defer closeSR()

	sigChan := make(chan os.Signal, 1)
	terminal.NotifyResize(sigChan)
	defer signal.Stop(sigChan)

	comp := screen.NewCompositor(80, 24)

	for {
		items := itemsProvider()
		query := strings.ToLower(searchQuery)
		var filtered []*SettingItem
		for _, item := range items {
			if searchQuery == "" && item.SearchOnly {
				continue
			}
			valStr := item.Value()
			match := query == "" ||
				strings.Contains(strings.ToLower(item.ID), query) ||
				strings.Contains(strings.ToLower(item.Name), query) ||
				strings.Contains(strings.ToLower(valStr), query) ||
				strings.Contains(strings.ToLower(item.Description), query)
			if match {
				filtered = append(filtered, item)
			}
		}

		if selectedIdx >= len(filtered) {
			selectedIdx = len(filtered) - 1
		}
		if selectedIdx < 0 {
			selectedIdx = 0
		}

		termW, termH := terminal.GetDimensions()
		if termW <= 0 {
			termW = 80
		}
		if termH <= 0 {
			termH = 24
		}

		menuRows, layout := renderSettingsMenuRows(title, searchQuery, filtered, selectedIdx, termW, termH, theme, extraRender)
		if err := FlushMenuScreen(comp, rlOutput, termW, termH, menuRows, screen.FlushOptions{}); err != nil {
			return err
		}
		useTwoCols, rows := layout.twoColumns, layout.rows

		keyData, resized, readErr := sr.ReadKeyOrResize(sigChan)
		if readErr != nil {
			return readErr
		}
		if resized {
			terminal.RefreshDimensions()
			continue
		}
		if AppendPastedQuery(keyData, &searchQuery, maxMenuInputBytes) {
			selectedIdx = 0
			continue
		}
		if len(keyData) == 0 {
			continue
		}

		key := string(keyData)
		if (termW < 18 || termH < 6) && key != "\x1b" && key != "\x03" && key != "\x04" {
			continue
		}

		switch key {
		case "\x1b[5~":
			selectedIdx = max(0, selectedIdx-layout.pageSize)
			continue
		case "\x1b[6~":
			selectedIdx = min(max(0, len(filtered)-1), selectedIdx+layout.pageSize)
			continue
		case "\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[7~":
			selectedIdx = 0
			continue
		case "\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~":
			selectedIdx = max(0, len(filtered)-1)
			continue
		case "\x1b[A", "\x1bOA":
			if len(filtered) > 0 {
				selectedIdx = (selectedIdx - 1 + len(filtered)) % len(filtered)
			}
			continue
		case "\x1b[B", "\x1bOB":
			if len(filtered) > 0 {
				selectedIdx = (selectedIdx + 1) % len(filtered)
			}
			continue
		case "\x1b[C", "\x1bOC":
			if len(filtered) > 0 && useTwoCols && selectedIdx < rows {
				target := min(selectedIdx+rows, len(filtered)-1)
				selectedIdx = target
			}
			continue
		case "\x1b[D", "\x1bOD":
			if len(filtered) > 0 && useTwoCols && selectedIdx >= rows {
				selectedIdx -= rows
			}
			continue
		case "\x03", "\x04":
			return fmt.Errorf("cancelled")
		case " ":
			if searchQuery == "" && len(filtered) > 0 && selectedIdx >= 0 && selectedIdx < len(filtered) {
				item := filtered[selectedIdx]
				if item.IsBool {
					if err := item.toggle(); err != nil {
						return err
					}
					if item.OnAction != nil || item.OnToggle != nil {
						comp.Invalidate()
					}
					continue
				}
			}
		case "\r", "\n":
			if len(filtered) > 0 && selectedIdx >= 0 && selectedIdx < len(filtered) {
				item := filtered[selectedIdx]
				if item.IsBool || item.OnToggle != nil || item.OnAction != nil {
					if err := item.toggle(); err != nil {
						return err
					}
					if item.OnAction != nil || item.OnToggle != nil {
						comp.Invalidate()
					}
				} else if item.OnEdit != nil {
					_, err := ReadMenuField(sr, sigChan, rlOutput, theme, "Edit "+item.Name, item.Description, "Value", item.Value(), item.OnEdit)
					comp.Invalidate()
					if err != nil && !errors.Is(err, ErrSelectionCancelled) {
						return err
					}
				}
			}
			continue
		case "\x1b":
			if searchQuery != "" {
				searchQuery = ""
				continue
			}
			return nil
		case "\x7f", "\x08":
			if len(searchQuery) > 0 {
				_, size := utf8.DecodeLastRuneInString(searchQuery)
				searchQuery = searchQuery[:len(searchQuery)-size]
			}
			continue
		}

		if keyData[0] != 27 {
			for i := 0; i < len(keyData); {
				r, size := utf8.DecodeRune(keyData[i:])
				if r == utf8.RuneError && size == 1 {
					i++
					continue
				}
				if r >= 32 && r != 127 && !unicode.IsControl(r) && len(searchQuery)+size <= maxMenuInputBytes {
					searchQuery += string(r)
				}
				i += size
			}
		}
	}
}

var ErrSelectionCancelled = errors.New("selection cancelled")

func RunInteractiveSelect(sr *SessionReader, signals chan os.Signal, output io.Writer, title string, items []string, theme style.UITheme) (int, error) {
	if len(items) == 0 {
		return -1, fmt.Errorf("no items to select")
	}
	selected, query := 0, ""
	comp := screen.NewCompositor(80, 24)
	for {
		var indexes []int
		for i, item := range items {
			if query == "" || strings.Contains(strings.ToLower(style.StripAnsi(item)), strings.ToLower(query)) {
				indexes = append(indexes, i)
			}
		}
		selected = max(0, min(selected, len(indexes)-1))
		width, height := terminal.GetDimensions()
		if width <= 0 {
			width = 80
		}
		if height <= 0 {
			height = 24
		}
		if width < 12 || height < 7 {
			rows := []string{style.TruncateCells("Resize terminal; Esc exits", max(1, width))}
			if err := FlushMenuScreen(comp, output, width, height, rows, screen.FlushOptions{}); err != nil {
				return -1, err
			}
			key, resized, err := sr.ReadKeyOrResize(signals)
			if err != nil {
				return -1, err
			}
			if resized {
				terminal.RefreshDimensions()
				continue
			}
			switch string(key) {
			case "\x1b", "\x03", "\x04":
				return -1, ErrSelectionCancelled
			}
			continue
		}
		visible := max(1, height-7)
		start := max(0, selected-visible+1)
		end := min(len(indexes), start+visible)
		rows := make([]string, 0, end-start+5)
		rows = append(rows,
			style.NewStyle().Foreground(theme.Primary).Bold(true).Render(style.TruncateCells(title, max(1, width-1))),
			style.TruncateCells("  Search: "+MenuDisplayText(query), max(1, width-1)),
			"",
		)
		if len(indexes) == 0 {
			rows = append(rows, "  No matching items")
		}
		for row := start; row < end; row++ {
			text := style.TruncateCells(strings.Join(strings.Fields(style.StripAnsi(items[indexes[row]])), " "), max(1, width-7))
			prefix := "    "
			tint := theme.Text
			if row == selected {
				prefix = "  › "
				tint = theme.Primary
			}
			rows = append(rows, prefix+style.NewStyle().Foreground(tint).Render(text))
		}
		rows = append(rows, "", style.TruncateCells("  ↑/↓ move · Enter select · type to filter · Esc back", max(1, width-1)))
		if err := FlushMenuScreen(comp, output, width, height, rows, screen.FlushOptions{}); err != nil {
			return -1, err
		}
		key, resized, err := sr.ReadKeyOrResize(signals)
		if err != nil {
			return -1, err
		}
		if resized {
			terminal.RefreshDimensions()
			continue
		}
		if AppendPastedQuery(key, &query, maxMenuInputBytes) {
			selected = 0
			continue
		}
		switch string(key) {
		case "\x1b", "\x03", "\x04":
			if query != "" && string(key) == "\x1b" {
				query = ""
				selected = 0
				continue
			}
			return -1, ErrSelectionCancelled
		case "\r", "\n":
			if len(indexes) > 0 {
				return indexes[selected], nil
			}
		case "\x1b[A", "\x1bOA":
			if len(indexes) > 0 {
				selected = (selected - 1 + len(indexes)) % len(indexes)
			}
		case "\x1b[B", "\x1bOB":
			if len(indexes) > 0 {
				selected = (selected + 1) % len(indexes)
			}
		case "\x1b[5~":
			selected = max(0, selected-visible)
		case "\x1b[6~":
			selected = min(max(0, len(indexes)-1), selected+visible)
		case "\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[7~":
			selected = 0
		case "\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~":
			selected = max(0, len(indexes)-1)
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
						if size > 0 && len(query)+size <= maxMenuInputBytes {
							query += string(r)
						}
					}
				}
				selected = 0
			}
		}
	}
}
