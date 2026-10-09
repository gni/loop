package render

import (
	"strings"
	"unicode/utf8"

	"loop/pkg/ui/style"
)

type PromptLayout struct {
	TotalRows   int
	ExtraOffset int
	CursorRow   int
	CursorCol   int
}

func NormalizeHistoryInput(input string, pos int) (string, int) {
	if !strings.Contains(input, "↵") {
		return input, pos
	}

	runes := []rune(input)
	var newRunes []rune
	newPos := pos

	i := 0
	for i < len(runes) {
		if runes[i] == '↵' {
			if len(newRunes) > 0 && newRunes[len(newRunes)-1] == ' ' {
				newRunes = newRunes[:len(newRunes)-1]
				if i < pos {
					newPos--
				}
			}
			newRunes = append(newRunes, '\n')
			if i+1 < len(runes) && runes[i+1] == ' ' {
				i++
				if i < pos {
					newPos--
				}
			}
		} else {
			newRunes = append(newRunes, runes[i])
		}
		i++
	}

	if newPos < 0 {
		newPos = 0
	}
	if newPos > len(newRunes) {
		newPos = len(newRunes)
	}

	return string(newRunes), newPos
}

// CalculatePromptLayout computes the exact physical terminal rows and cursor position
// for a given prompt input string (including multi-line and wrapped text) at a given terminal width.
func CalculatePromptLayout(promptPrefix string, inputLine string, pos int, termWidth int) PromptLayout {
	inputLine, pos = NormalizeHistoryInput(inputLine, pos)

	if termWidth <= 0 {
		termWidth = 80
	}

	prefixRunes := utf8.RuneCountInString(style.StripAnsi(promptPrefix))
	lines := strings.Split(inputLine, "\n")

	totalRows := 0
	cursorRow := 0
	cursorCol := 1
	posFound := false
	currentRuneCount := 0

	for lineIdx, lineStr := range lines {
		lineRunes := []rune(lineStr)
		lineLen := len(lineRunes)

		startColOffset := 0
		if lineIdx == 0 {
			startColOffset = prefixRunes
		}

		lineTotalRunes := startColOffset + lineLen
		rowsForLine := (lineTotalRunes + termWidth - 1) / termWidth
		if rowsForLine < 1 {
			rowsForLine = 1
		}

		if !posFound {
			if pos <= currentRuneCount+lineLen {
				offsetInLine := pos - currentRuneCount
				if lineIdx == 0 {
					offsetInLine += prefixRunes
				}
				rem := offsetInLine % termWidth
				if rem == 0 && offsetInLine > 0 {
					cursorCol = termWidth
					cursorRow = totalRows + (offsetInLine / termWidth) - 1
				} else {
					cursorCol = rem + 1
					cursorRow = totalRows + (offsetInLine / termWidth)
				}
				posFound = true
			} else {
				currentRuneCount += lineLen + 1 // +1 for \n
			}
		}

		totalRows += rowsForLine
	}

	if !posFound {
		totalRunes := prefixRunes + utf8.RuneCountInString(inputLine)
		cursorRow = totalRows - 1
		if cursorRow < 0 {
			cursorRow = 0
		}
		rem := totalRunes % termWidth
		if rem == 0 && totalRunes > 0 {
			cursorCol = termWidth
		} else {
			cursorCol = rem + 1
		}
	}

	extraOffset := totalRows - 1
	if extraOffset < 0 {
		extraOffset = 0
	}

	return PromptLayout{
		TotalRows:   totalRows,
		ExtraOffset: extraOffset,
		CursorRow:   cursorRow,
		CursorCol:   cursorCol,
	}
}

