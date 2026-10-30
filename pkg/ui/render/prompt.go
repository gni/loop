package render

import (
	"strings"
)

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
