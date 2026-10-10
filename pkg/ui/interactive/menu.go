package interactive

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

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

// GetInteractiveIO resolves the appropriate input reader and output writer for
// interactive modals, falling back to /dev/tty if available, and returning
// a cleanup callback to close any opened file handles.
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

func runSettingsMenuLoop(rlInput io.Reader, rlOutput io.Writer, theme style.UITheme, title string, itemsProvider func() []*settingItem, extraRender func(buf *strings.Builder)) error {
	searchQuery := ""
	selectedIdx := 0

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

		var buf strings.Builder
		buf.WriteString("\x1b[H")

		titleStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		buf.WriteString(titleStyle.Render(title))
		buf.WriteString("\n\n")

		searchLabelStyle := style.NewStyle().Foreground(theme.Text)
		buf.WriteString(searchLabelStyle.Render("  search:  "))

		searchValStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
		buf.WriteString(searchValStyle.Render(searchQuery))
		buf.WriteString("\n")

		underlineStyle := style.NewStyle().Foreground(theme.Border)
		buf.WriteString(underlineStyle.Render("           ────────────────────"))
		buf.WriteString("\n\n")

		if len(filtered) == 0 {
			dimStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
			buf.WriteString(dimStyle.Render("  (no matching settings found)"))
			buf.WriteString("\n")
		} else {
			for idx, item := range filtered {
				nameStr := item.name
				valStr := item.value()

				keyColWidth := 28
				nameLen := len(nameStr)
				leader := ""
				if nameLen < keyColWidth {
					leader = strings.Repeat("·", keyColWidth-nameLen)
				}

				if idx == selectedIdx {
					markerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
					nameStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
					leaderStyle := style.NewStyle().Foreground(theme.Border)
					valStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
					bracketStyle := style.NewStyle().Foreground(theme.Secondary)

					valStrFormatted := ""
					if valStr != "" {
						valStrFormatted = fmt.Sprintf("%s %s %s", bracketStyle.Render("["), valStyle.Render(valStr), bracketStyle.Render("]"))
					}
					buf.WriteString(fmt.Sprintf("%s  %s %s %s\n", markerStyle.Render("›"), nameStyle.Render(nameStr), leaderStyle.Render(leader), valStrFormatted))
				} else {
					nameStyle := style.NewStyle().Foreground(theme.Text)
					leaderStyle := style.NewStyle().Foreground(theme.Border)
					valStyle := style.NewStyle().Foreground(theme.Secondary)
					buf.WriteString(fmt.Sprintf("   %s %s %s\n", nameStyle.Render(nameStr), leaderStyle.Render(leader), valStyle.Render(valStr)))
				}
			}
		}

		buf.WriteString("\n")

		if len(filtered) > 0 && selectedIdx >= 0 && selectedIdx < len(filtered) {
			descStyle := style.NewStyle().Foreground(theme.Success)
			buf.WriteString(fmt.Sprintf("  %s\n", descStyle.Render(filtered[selectedIdx].description)))
		} else {
			buf.WriteString("\n")
		}

		if extraRender != nil {
			extraRender(&buf)
		}

		buf.WriteString("\n")
		navStyle := style.NewStyle().Foreground(theme.Border)
		buf.WriteString(fmt.Sprintf("  %s\n", navStyle.Render("↑/↓ navigate · enter select/edit · esc clear search/exit")))
		buf.WriteString(fmt.Sprintf("  %s\n", navStyle.Render("esc to save and exit")))

		buf.WriteString("\x1b[J")

		outputStr := strings.ReplaceAll(buf.String(), "\n", "\x1b[K\r\n")
		_, _ = rlOutput.Write([]byte(outputStr))

		var readBuf [16]byte
		n, err := rlInput.Read(readBuf[:])
		if err != nil {
			return err
		}

		if n == 1 {
			char := readBuf[0]

			if char == 3 || char == 4 {
				return fmt.Errorf("cancelled")
			}

			if char == 13 || char == 10 {
				if len(filtered) > 0 && selectedIdx >= 0 && selectedIdx < len(filtered) {
					item := filtered[selectedIdx]
					if item.onToggle != nil {
						item.onToggle()
					} else if item.onEdit != nil {
						fmt.Fprintf(rlOutput, "\r\n\r\n  edit %s (current: %s):\r\n", item.name, item.value())
						fmt.Fprint(rlOutput, "  enter new value (empty to delete if header): ")

						newVal, err := ReadInputRaw(rlInput, rlOutput)
						if err == nil {
							newVal = strings.TrimSpace(newVal)
							err = item.onEdit(newVal)
							if err != nil {
								fmt.Fprintf(rlOutput, "\r\n  error: %v. press enter to continue...", err)
								_, _ = ReadInputRaw(rlInput, rlOutput)
							}
						}
					}
				}
				continue
			}

			if char == 27 {
				if searchQuery != "" {
					searchQuery = ""
				} else {
					return nil
				}
				continue
			}

			if char == 127 || char == 8 {
				if len(searchQuery) > 0 {
					searchQuery = searchQuery[:len(searchQuery)-1]
				}
				continue
			}

			if char >= 32 && char <= 126 {
				searchQuery += string(char)
				continue
			}
		}

		if n >= 3 && readBuf[0] == 27 && readBuf[1] == '[' {
			switch readBuf[2] {
			case 'A':
				if len(filtered) > 0 {
					selectedIdx = (selectedIdx - 1 + len(filtered)) % len(filtered)
				}
			case 'B':
				if len(filtered) > 0 {
					selectedIdx = (selectedIdx + 1) % len(filtered)
				}
			}
		}
	}
}

func RunInteractiveSelect(sr *SessionReader, sigChan chan os.Signal, rlOutput io.Writer, title string, items []string, theme style.UITheme) (int, error) {
	return runInteractiveSelect(sr, sigChan, rlOutput, title, items, theme)
}

func runInteractiveSelect(sr *sessionReader, sigChan chan os.Signal, rlOutput io.Writer, title string, items []string, theme style.UITheme) (int, error) {
	selectedIdx := 0

	for {
		var buf strings.Builder
		buf.WriteString("\x1b[H")

		titleStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		buf.WriteString(titleStyle.Render(title))
		buf.WriteString("\n\n")

		for idx, item := range items {
			if idx == selectedIdx {
				markerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
				itemStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
				buf.WriteString(fmt.Sprintf("    %s %s\n", markerStyle.Render("›"), itemStyle.Render(item)))
			} else {
				itemStyle := style.NewStyle().Foreground(theme.Text)
				buf.WriteString(fmt.Sprintf("      %s\n", itemStyle.Render(item)))
			}
		}

		buf.WriteString("\n")
		navStyle := style.NewStyle().Foreground(theme.Border)
		buf.WriteString(fmt.Sprintf("  %s\n", navStyle.Render("↑/↓ navigate · enter select · esc cancel")))

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
			char := readBuf[0]
			if char == 3 || char == 27 || char == 4 {
				return -1, fmt.Errorf("selection cancelled")
			}
			if char == 13 || char == 10 {
				return selectedIdx, nil
			}
		}

		if n >= 3 && readBuf[0] == 27 && readBuf[1] == '[' {
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
