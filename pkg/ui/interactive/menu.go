package interactive

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"

	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

type settingItem struct {
	id          string
	name        string
	value       func() string
	description string
	options     []string
	isBool      bool
	onToggle    func()
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
		nameStr = style.TruncateRunes(nameStr, keyColWidth-1)
	}

	if colWidth > 0 && !hasAnsiVal {
		maxValRunes := colWidth - keyColWidth - 6
		if maxValRunes < 6 {
			maxValRunes = 6
		}
		if utf8.RuneCountInString(valStr) > maxValRunes {
			valStr = style.TruncateRunes(valStr, maxValRunes)
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
	vis := utf8.RuneCountInString(style.StripAnsi(cell))
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
		var filtered []*settingItem
		for _, item := range items {
			valStr := item.value()
			match := searchQuery == "" ||
				strings.Contains(strings.ToLower(item.name), strings.ToLower(searchQuery)) ||
				strings.Contains(strings.ToLower(valStr), strings.ToLower(searchQuery)) ||
				strings.Contains(strings.ToLower(item.description), strings.ToLower(searchQuery))
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

		// Provider profiles are contiguous groups of fields. Splitting the
		// flattened list in half can separate a provider from its settings.
		providerMenu := title == "endpoint providers config"
		useTwoCols := !providerMenu && termW >= 72 && len(filtered) > 1
		rows := len(filtered)
		if useTwoCols {
			rows = (len(filtered) + 1) / 2
		}

		var buf strings.Builder
		buf.WriteString("\x1b[H")

		titleStyled := style.NewStyle().Foreground(theme.Primary).Bold(true).Render(title)
		searchLabel := style.NewStyle().Foreground(theme.Text).Render("  search: ")
		searchVal := style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(searchQuery)
		underline := style.NewStyle().Foreground(theme.Border).Render("──────────")
		if searchQuery == "" {
			buf.WriteString(fmt.Sprintf("%s    %s%s\n\n", titleStyled, searchLabel, underline))
		} else {
			buf.WriteString(fmt.Sprintf("%s    %s%s\n\n", titleStyled, searchLabel, searchVal))
		}

		if len(filtered) == 0 {
			buf.WriteString(style.NewStyle().Foreground(theme.Border).Italic(true).Render("  (no matching settings found)"))
			buf.WriteString("\n")
		} else if useTwoCols {
			colWidth := (termW - 5) / 2
			sep := style.NewStyle().Foreground(theme.Border).Render(" │ ")

			maxVisibleRows := termH - 8
			if extraRender != nil && termH >= 22 {
				maxVisibleRows = termH - 11
			}
			if maxVisibleRows < 5 {
				maxVisibleRows = 5
			}

			selectedRow := selectedIdx % rows
			startRow := 0
			if rows > maxVisibleRows {
				if selectedRow >= startRow+maxVisibleRows {
					startRow = selectedRow - maxVisibleRows + 1
				}
				if selectedRow < startRow {
					startRow = selectedRow
				}
				if startRow+maxVisibleRows > rows {
					startRow = rows - maxVisibleRows
				}
			}
			endRow := rows
			if endRow > startRow+maxVisibleRows {
				endRow = startRow + maxVisibleRows
			}

			for r := startRow; r < endRow; r++ {
				leftIdx := r
				rightIdx := r + rows

				leftCell := formatMenuCell(filtered[leftIdx], leftIdx == selectedIdx, colWidth, theme)
				if rightIdx < len(filtered) {
					rightCell := formatMenuCell(filtered[rightIdx], rightIdx == selectedIdx, colWidth, theme)
					buf.WriteString(padAnsiCell(leftCell, colWidth) + sep + rightCell + "\n")
				} else {
					buf.WriteString(leftCell + "\n")
				}
			}
		} else {
			start, end := 0, len(filtered)
			if providerMenu {
				// Leave room for the selected field's untruncated value and
				// the description/navigation footer, even with many providers.
				visibleRows := termH - 8
				if termH >= 14 && len(filtered) > 0 {
					valueWidth := termW - 8
					if valueWidth < 1 {
						valueWidth = 1
					}
					valueLen := utf8.RuneCountInString(filtered[selectedIdx].value())
					if valueLen > 0 {
						valueRows := (valueLen + valueWidth - 1) / valueWidth
						if valueRows > 3 {
							valueRows = 3
						}
						visibleRows -= valueRows + 1
					}
				}
				if visibleRows < 1 {
					visibleRows = 1
				}
				if len(filtered) > visibleRows {
					start = selectedIdx - visibleRows + 1
					if start < 0 {
						start = 0
					}
					if start+visibleRows > len(filtered) {
						start = len(filtered) - visibleRows
					}
					end = start + visibleRows
				}
			}
			for idx := start; idx < end; idx++ {
				buf.WriteString(formatMenuCell(filtered[idx], idx == selectedIdx, termW-4, theme) + "\n")
			}
		}

		if len(filtered) > 0 && selectedIdx >= 0 && selectedIdx < len(filtered) {
			descStyle := style.NewStyle().Foreground(theme.Success)
			buf.WriteString(fmt.Sprintf("\n  %s\n", descStyle.Render(filtered[selectedIdx].description)))
		} else {
			buf.WriteString("\n")
		}

		if providerMenu {
			// The overview duplicates the provider fields and can push the
			// next provider out of view. Show the complete selected value
			// instead; formatMenuCell intentionally shortens long values.
			if termH >= 14 && len(filtered) > 0 {
				value := []rune(filtered[selectedIdx].value())
				if len(value) > 0 {
					valueWidth := termW - 8
					if valueWidth < 1 {
						valueWidth = 1
					}
					buf.WriteString("  " + style.NewStyle().Foreground(theme.Secondary).Render("value:") + "\n")
					for row := 0; row < 3 && len(value) > 0; row++ {
						n := valueWidth
						if n > len(value) {
							n = len(value)
						}
						line := string(value[:n])
						value = value[n:]
						if row == 2 && len(value) > 0 && n > 1 {
							line = string([]rune(line)[:n-1]) + "…"
						}
						buf.WriteString("    " + style.NewStyle().Foreground(theme.Text).Render(line) + "\n")
					}
				}
			}
		} else if extraRender != nil && termH >= 20 {
			extraRender(&buf)
		}

		navStyle := style.NewStyle().Foreground(theme.Border)
		buf.WriteString(fmt.Sprintf("  %s\n", navStyle.Render("↑/↓/←/→ navigate · enter select/edit · type to search · esc save & exit")))
		buf.WriteString("\x1b[J")

		outputStr := strings.ReplaceAll(buf.String(), "\n", "\x1b[K\r\n")
		_, _ = rlOutput.Write([]byte(outputStr))

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
					if item.isBool && item.onToggle != nil {
						item.onToggle()
						continue
					}
				}
			case 13, 10:
				if len(filtered) > 0 && selectedIdx >= 0 && selectedIdx < len(filtered) {
					item := filtered[selectedIdx]
					if item.onToggle != nil {
						item.onToggle()
					} else if item.onEdit != nil {
						fmt.Fprintf(rlOutput, "\r\n  edit %s (current: %s): ", item.name, item.value())
						newVal, err := ReadInputRaw(rlInput, rlOutput)
						if err == nil {
							if err = item.onEdit(strings.TrimSpace(newVal)); err != nil {
								fmt.Fprintf(rlOutput, "\r\n  error: %v. press enter to continue...", err)
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

func RunInteractiveSelect(sr *SessionReader, sigChan chan os.Signal, rlOutput io.Writer, title string, items []string, theme style.UITheme) (int, error) {
	if len(items) == 0 {
		return -1, fmt.Errorf("no items to select")
	}
	selectedIdx := 0

	for {
		var buf strings.Builder
		buf.WriteString("\x1b[H")
		buf.WriteString(style.NewStyle().Foreground(theme.Primary).Bold(true).Render(title))
		buf.WriteString("\n\n")

		width, height := terminal.GetDimensions()
		if width < 1 {
			width = 80
		}
		if height < 1 {
			height = 24
		}
		visible := height - 6
		if visible < 1 {
			visible = 1
		}
		start := selectedIdx - visible + 1
		if start < 0 {
			start = 0
		}
		end := start + visible
		if end > len(items) {
			end = len(items)
		}
		for idx := start; idx < end; idx++ {
			item := style.TruncateRunes(strings.Join(strings.Fields(items[idx]), " "), max(1, width-8))
			if idx == selectedIdx {
				primaryBold := style.NewStyle().Foreground(theme.Primary).Bold(true)
				buf.WriteString(fmt.Sprintf("    %s %s\n", primaryBold.Render("›"), primaryBold.Render(item)))
			} else {
				buf.WriteString(fmt.Sprintf("      %s\n", style.NewStyle().Foreground(theme.Text).Render(item)))
			}
		}

		buf.WriteString("\n")
		buf.WriteString(fmt.Sprintf("  %s\n", style.NewStyle().Foreground(theme.Border).Render("↑/↓ navigate · enter select · esc cancel")))
		buf.WriteString("\x1b[J")

		outputStr := strings.ReplaceAll(buf.String(), "\n", "\x1b[K\r\n")
		_, _ = rlOutput.Write([]byte(outputStr))

		readBuf, resized, err := sr.ReadKeyOrResize(sigChan)
		if err != nil {
			return -1, err
		}
		if resized {
			continue
		}
		n := len(readBuf)

		if n == 1 {
			switch readBuf[0] {
			case 3, 27, 4:
				return -1, ErrSelectionCancelled
			case 13, 10:
				return selectedIdx, nil
			}
		}

		if n >= 3 && readBuf[0] == 27 && (readBuf[1] == '[' || readBuf[1] == 'O') {
			switch readBuf[2] {
			case 'A':
				selectedIdx = (selectedIdx - 1 + len(items)) % len(items)
			case 'B':
				selectedIdx = (selectedIdx + 1) % len(items)
			}
		}
	}
}

func parsePositiveInt(newVal string, target *int) error {
	if newVal == "" {
		return nil
	}
	n, err := strconv.Atoi(newVal)
	if err != nil || n <= 0 {
		return fmt.Errorf("must be a positive integer")
	}
	*target = n
	return nil
}
