package style

import (
	"fmt"
	"image/color"
	"strings"
	"unicode/utf8"
)

const (
	noBorder = iota
	roundedBorder
)

func RoundedBorder() int {
	return roundedBorder
}

const (
	Center = iota
	Left
	Right
	Top
	Bottom
)

type Style struct {
	fg            *colorVal
	bg            *colorVal
	bold          bool
	italic        bool
	underline     bool
	borderType    int
	borderColor   *colorVal
	paddingLeft   int
	paddingRight  int
	paddingTop    int
	paddingBottom int
	marginLeft    int
	marginRight   int
	marginTop     int
	marginBottom  int
	maxWidth      int
}

func NewStyle() Style {
	return Style{}
}

func (s Style) MaxWidth(v int) Style {
	s.maxWidth = v
	return s
}

func toColorVal(c color.Color) *colorVal {
	if lc, ok := c.(colorVal); ok {
		return &lc
	} else if c != nil {
		r, g, b, _ := c.RGBA()
		return &colorVal{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8)}
	}
	return nil
}

func parseBoxSpacing(args []int) (top, right, bottom, left int) {
	if len(args) == 1 {
		return args[0], args[0], args[0], args[0]
	} else if len(args) == 2 {
		return args[0], args[1], args[0], args[1]
	} else if len(args) == 4 {
		return args[0], args[1], args[2], args[3]
	}
	return 0, 0, 0, 0
}

func (s Style) Foreground(c color.Color) Style {
	s.fg = toColorVal(c)
	return s
}

func (s Style) Bold(v bool) Style {
	s.bold = v
	return s
}

func (s Style) Italic(v bool) Style {
	s.italic = v
	return s
}

func (s Style) Underline(v bool) Style {
	s.underline = v
	return s
}

func (s Style) GetSequence() (string, string) {
	ansiStart := ""
	ansiEnd := ""
	if s.bold {
		ansiStart += "\x1b[1m"
	}
	if s.italic {
		ansiStart += "\x1b[3m"
	}
	if s.underline {
		ansiStart += "\x1b[4m"
	}
	if s.fg != nil {
		ansiStart += fmt.Sprintf("\x1b[38;2;%d;%d;%dm", s.fg.R, s.fg.G, s.fg.B)
	}
	if s.bg != nil {
		ansiStart += fmt.Sprintf("\x1b[48;2;%d;%d;%dm", s.bg.R, s.bg.G, s.bg.B)
	}
	if ansiStart != "" {
		ansiEnd = "\x1b[0m"
	}
	return ansiStart, ansiEnd
}

func (s Style) MarginLeft(v int) Style {
	s.marginLeft = v
	return s
}

func (s Style) Margin(args ...int) Style {
	s.marginTop, s.marginRight, s.marginBottom, s.marginLeft = parseBoxSpacing(args)
	return s
}

func (s Style) Padding(args ...int) Style {
	s.paddingTop, s.paddingRight, s.paddingBottom, s.paddingLeft = parseBoxSpacing(args)
	return s
}

func (s Style) Border(b interface{}, args ...bool) Style {
	if bt, ok := b.(int); ok {
		s.borderType = bt
	} else {
		s.borderType = roundedBorder
	}
	return s
}

func (s Style) BorderForeground(c color.Color) Style {
	s.borderColor = toColorVal(c)
	return s
}

func (s Style) Render(args ...string) string {
	str := strings.Join(args, " ")

	if s.maxWidth > 0 {
		contentMaxWidth := s.maxWidth
		if s.borderType != noBorder {
			contentMaxWidth -= 2
		}
		contentMaxWidth -= s.paddingLeft + s.paddingRight
		contentMaxWidth -= s.marginLeft + s.marginRight
		if contentMaxWidth < 10 {
			contentMaxWidth = 10
		}

		var wrappedLines []string
		lines := strings.Split(str, "\n")
		for _, line := range lines {
			wrappedLines = append(wrappedLines, wrapSingleAnsiLine(line, contentMaxWidth)...)
		}
		str = strings.Join(wrappedLines, "\n")
	}

	lines := strings.Split(str, "\n")

	ansiStart := ""
	ansiEnd := ""
	if s.bold {
		ansiStart += "\x1b[1m"
	}
	if s.italic {
		ansiStart += "\x1b[3m"
	}
	if s.underline {
		ansiStart += "\x1b[4m"
	}
	if s.fg != nil {
		ansiStart += fmt.Sprintf("\x1b[38;2;%d;%d;%dm", s.fg.R, s.fg.G, s.fg.B)
	}
	if s.bg != nil {
		ansiStart += fmt.Sprintf("\x1b[48;2;%d;%d;%dm", s.bg.R, s.bg.G, s.bg.B)
	}
	if ansiStart != "" {
		ansiEnd = "\x1b[0m"
	}

	maxWidth := 0
	for _, line := range lines {
		w := utf8.RuneCountInString(StripAnsi(line))
		if w > maxWidth {
			maxWidth = w
		}
	}

	paddedWidth := maxWidth + s.paddingLeft + s.paddingRight
	var formattedLines []string
	for _, line := range lines {
		runeCount := utf8.RuneCountInString(StripAnsi(line))
		padL := strings.Repeat(" ", s.paddingLeft)
		var padR string
		if s.bg != nil || s.borderType != noBorder {
			padR = strings.Repeat(" ", s.paddingRight+(maxWidth-runeCount))
		} else {
			padR = strings.Repeat(" ", s.paddingRight)
		}
		formattedLines = append(formattedLines, padL+line+padR)
	}

	var borderedLines []string
	borderStart := ""
	borderEnd := ""
	if s.borderColor != nil {
		borderStart = fmt.Sprintf("\x1b[38;2;%d;%d;%dm", s.borderColor.R, s.borderColor.G, s.borderColor.B)
		borderEnd = "\x1b[0m"
	}

	if s.borderType != noBorder {
		var tl, tr, bl, br, h, v string
		if s.borderType == roundedBorder {
			tl, tr, bl, br, h, v = "╭", "╮", "╰", "╯", "─", "│"
		} else {
			tl, tr, bl, br, h, v = "┌", "┐", "└", "┘", "─", "│"
		}

		top := borderStart + tl + strings.Repeat(h, paddedWidth) + tr + borderEnd
		borderedLines = append(borderedLines, top)

		padLine := borderStart + v + borderEnd + strings.Repeat(" ", paddedWidth) + borderStart + v + borderEnd
		for i := 0; i < s.paddingTop; i++ {
			borderedLines = append(borderedLines, padLine)
		}

		for _, line := range formattedLines {
			contentLine := borderStart + v + borderEnd + ansiStart + line + ansiEnd + borderStart + v + borderEnd
			borderedLines = append(borderedLines, contentLine)
		}

		for i := 0; i < s.paddingBottom; i++ {
			borderedLines = append(borderedLines, padLine)
		}

		bottom := borderStart + bl + strings.Repeat(h, paddedWidth) + br + borderEnd
		borderedLines = append(borderedLines, bottom)
	} else {
		for _, line := range formattedLines {
			borderedLines = append(borderedLines, ansiStart+line+ansiEnd)
		}
	}

	var marginLines []string
	for i := 0; i < s.marginTop; i++ {
		marginLines = append(marginLines, "")
	}
	for _, line := range borderedLines {
		marginLines = append(marginLines, strings.Repeat(" ", s.marginLeft)+line+strings.Repeat(" ", s.marginRight))
	}
	for i := 0; i < s.marginBottom; i++ {
		marginLines = append(marginLines, "")
	}

	return strings.Join(marginLines, "\n")
}

func JoinHorizontal(pos int, strs ...string) string {
	if len(strs) == 0 {
		return ""
	}
	if len(strs) == 1 {
		return strs[0]
	}

	var splitStrs [][]string
	maxLines := 0
	for _, s := range strs {
		lines := strings.Split(s, "\n")
		splitStrs = append(splitStrs, lines)
		if len(lines) > maxLines {
			maxLines = len(lines)
		}
	}

	var widths []int
	for _, lines := range splitStrs {
		maxW := 0
		for _, l := range lines {
			w := utf8.RuneCountInString(StripAnsi(l))
			if w > maxW {
				maxW = w
			}
		}
		widths = append(widths, maxW)
	}

	var joinedLines []string
	for i := 0; i < maxLines; i++ {
		var lineParts []string
		for idx, lines := range splitStrs {
			w := widths[idx]
			var lineVal string
			if i < len(lines) {
				lineVal = lines[i]
			}
			visibleW := utf8.RuneCountInString(StripAnsi(lineVal))
			padR := strings.Repeat(" ", w-visibleW)
			lineParts = append(lineParts, lineVal+padR)
		}
		joinedLines = append(joinedLines, strings.Join(lineParts, ""))
	}

	return strings.Join(joinedLines, "\n")
}

