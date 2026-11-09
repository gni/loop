package interactive

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"

	"loop/pkg/terminal"
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

type settingItem struct {
	searchOnly  bool
	id          string
	name        string
	value       func() string
	description string
	options     []string
	isBool      bool
	onToggle    func()
	onAction    func() error
	onEdit      func(newVal string) error
}

func withAltScreenTerminal(rlInput io.Reader, rlOutput io.Writer, fn func() error) (bool, error) {
	var fd int
	if f, ok := rlInput.(*os.File); ok {
		fd = int(f.Fd())
	} else {
		fd = int(os.Stdin.Fd())
	}

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

	return true, fn()
}

func GetInteractiveIO(kiReader io.Reader) (io.Reader, io.Writer, func()) {
	var input io.Reader = kiReader
	var inputCloser io.Closer
	if input == nil {
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

func formatMenuCell(item *settingItem, selected bool, colWidth int, theme style.UITheme) string {
	nameStr := item.name
	valStr := item.value()
	cleanVal := style.StripAnsi(valStr)
	hasAnsiVal := cleanVal != valStr

	keyColWidth := 24
	if colWidth > 0 {
		keyColWidth = colWidth - 16
		if keyColWidth < 14 {
			keyColWidth = 14
		}
		if keyColWidth > 26 {
			keyColWidth = 26
		}
	}

	if utf8.RuneCountInString(nameStr) > keyColWidth-1 {
		nameStr = style.TruncateCells(nameStr, keyColWidth-1)
	}

	if colWidth > 0 && !hasAnsiVal {
		maxValRunes := colWidth - keyColWidth - 6
		if maxValRunes < 6 {
			maxValRunes = 6
		}
		if utf8.RuneCountInString(valStr) > maxValRunes {
			valStr = style.TruncateCells(valStr, maxValRunes)
		}
	}

	nameLen := utf8.RuneCountInString(nameStr)
	leader := ""
	if nameLen < keyColWidth {
		leader = strings.Repeat("·", keyColWidth-nameLen)
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

func padAnsiCell(cell string, width int) string {
	vis := style.CellWidth(cell)
	if vis >= width {
		return cell
	}
	return cell + strings.Repeat(" ", width-vis)
}

func runSettingsMenuLoop(rlInput io.Reader, rlOutput io.Writer, theme style.UITheme, title string, itemsProvider func() []*settingItem, extraRender func(buf *strings.Builder)) error {
	searchQuery := ""
	selectedIdx := 0

	var sigChan chan os.Signal
	sr, hasSessionReader := rlInput.(*SessionReader)
	if hasSessionReader {
		sigChan = make(chan os.Signal, 1)
		terminal.NotifyResize(sigChan)
		defer signal.Stop(sigChan)
	}

	for {
		items := itemsProvider()
		query := strings.ToLower(searchQuery)
		var filtered []*settingItem
		for _, item := range items {
			if searchQuery == "" && item.searchOnly {
				continue
			}
			valStr := item.value()
			match := query == "" ||
				strings.Contains(strings.ToLower(item.id), query) ||
				strings.Contains(strings.ToLower(item.name), query) ||
				strings.Contains(strings.ToLower(valStr), query) ||
				strings.Contains(strings.ToLower(item.description), query)
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

		frame, layout := renderSettingsMenu(title, searchQuery, filtered, selectedIdx, termW, termH, theme, extraRender)
		if err := writeMenuFrame(rlOutput, frame); err != nil {
			return err
		}
		useTwoCols, rows := layout.twoColumns, layout.rows

		var readBuf [16]byte
		var n int
		var err error
		if hasSessionReader {
			keyData, resized, readErr := sr.ReadKeyOrResize(sigChan)
			if readErr != nil {
				return readErr
			}
			if resized {
				continue
			}
			n = copy(readBuf[:], keyData)
		} else {
			n, err = rlInput.Read(readBuf[:])
			if err != nil {
				return err
			}
		}
		if n == 0 {
			continue
		}

		key := string(readBuf[:n])
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
		}
		if n >= 3 && readBuf[0] == 27 && (readBuf[1] == '[' || readBuf[1] == 'O') {
			if len(filtered) > 0 {
				switch readBuf[2] {
				case 'A':
					selectedIdx = (selectedIdx - 1 + len(filtered)) % len(filtered)
				case 'B':
					selectedIdx = (selectedIdx + 1) % len(filtered)
				case 'C':
					if useTwoCols && selectedIdx < rows {
						target := selectedIdx + rows
						if target >= len(filtered) {
							target = len(filtered) - 1
						}
						selectedIdx = target
					}
				case 'D':
					if useTwoCols && selectedIdx >= rows {
						selectedIdx -= rows
					}
				}
			}
			continue
		}

		if n == 1 {
			char := readBuf[0]
			switch char {
			case 3, 4:
				return fmt.Errorf("cancelled")
			case ' ':
				if searchQuery == "" && len(filtered) > 0 && selectedIdx >= 0 && selectedIdx < len(filtered) {
					item := filtered[selectedIdx]
					if item.isBool {
						if err := item.toggle(); err != nil {
							return err
						}
						continue
					}
				}
			case 13, 10:
				if len(filtered) > 0 && selectedIdx >= 0 && selectedIdx < len(filtered) {
					item := filtered[selectedIdx]
					if item.isBool || item.onToggle != nil || item.onAction != nil {
						if err := item.toggle(); err != nil {
							return err
						}
					} else if item.onEdit != nil {
						if hasSessionReader {
							_, err := readMenuField(sr, sigChan, rlOutput, theme, "Edit "+item.name, item.description, "Value", item.value(), item.onEdit)
							if err != nil && !errors.Is(err, ErrSelectionCancelled) {
								return err
							}
							continue
						}
						if err := writeMenuFrame(rlOutput, fmt.Sprintf("\r\n  edit %s (current: %s): ", item.name, item.value())); err != nil {
							return err
						}
						newVal, err := ReadInputRaw(rlInput, rlOutput)
						if err == nil {
							if err = item.onEdit(strings.TrimSpace(newVal)); err != nil {
								if err := writeMenuFrame(rlOutput, fmt.Sprintf("\r\n  error: %v. press enter to continue...", err)); err != nil {
									return err
								}
								_, _ = ReadInputRaw(rlInput, rlOutput)
							}
						}
					}
				}
				continue
			case 27:
				if searchQuery != "" {
					searchQuery = ""
					continue
				}
				return nil
			case 127, 8:
				if len(searchQuery) > 0 {
					_, size := utf8.DecodeLastRuneInString(searchQuery)
					searchQuery = searchQuery[:len(searchQuery)-size]
				}
				continue
			}
		}

		if readBuf[0] != 27 {
			for i := 0; i < n; {
				r, size := utf8.DecodeRune(readBuf[i:n])
				if r == utf8.RuneError && size == 1 {
					i++
					continue
				}
				if r >= 32 && r != 127 {
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
	for {
		var indexes []int
		for i, item := range items {
			if query == "" || strings.Contains(strings.ToLower(style.StripAnsi(item)), strings.ToLower(query)) {
				indexes = append(indexes, i)
			}
		}
		selected = max(0, min(selected, len(indexes)-1))
		width, height := terminal.GetDimensions()
		if width < 12 || height < 7 {
			if err := writeMenuFrame(output, "\x1b[H\x1b[J"+style.TruncateCells("Resize terminal; Esc exits", max(1, width-1))); err != nil {
				return -1, err
			}
			key, _, err := sr.ReadKeyOrResize(signals)
			if err != nil {
				return -1, err
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
		var screen strings.Builder
		screen.WriteString("\x1b[H" + style.NewStyle().Foreground(theme.Primary).Bold(true).Render(style.TruncateCells(title, max(1, width-1))) + "\n")
		screen.WriteString(style.TruncateCells("  Search: "+query, max(1, width-1)) + "\n\n")
		if len(indexes) == 0 {
			screen.WriteString("  No matching items\n")
		}
		for row := start; row < end; row++ {
			text := style.TruncateCells(strings.Join(strings.Fields(style.StripAnsi(items[indexes[row]])), " "), max(1, width-7))
			prefix := "    "
			tint := theme.Text
			if row == selected {
				prefix = "  › "
				tint = theme.Primary
			}
			screen.WriteString(prefix + style.NewStyle().Foreground(tint).Render(text) + "\n")
		}
		screen.WriteString("\n" + style.TruncateCells("  ↑/↓ move · Enter select · type to filter · Esc back", max(1, width-1)) + "\x1b[J")
		if err := writeMenuFrame(output, strings.ReplaceAll(screen.String(), "\n", "\x1b[K\r\n")); err != nil {
			return -1, err
		}
		key, resized, err := sr.ReadKeyOrResize(signals)
		if err != nil {
			return -1, err
		}
		if resized {
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
		case "\x7f", "\x08":
			if len(query) > 0 {
				_, size := utf8.DecodeLastRuneInString(query)
				query = query[:len(query)-size]
			}
			selected = 0
		default:
			if len(key) > 0 && key[0] != 27 {
				for _, r := range string(key) {
					if r >= 32 && r != 127 {
						query += string(r)
					}
				}
				selected = 0
			}
		}
	}
}
