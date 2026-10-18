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

func ActivityDivider(theme UITheme) string {
	return NewStyle().Foreground(theme.Border).Render(strings.Repeat("· ", 19) + "·")
}

func ActivitySpinnerFrame(theme UITheme, frameIndex int) string {
	active := NewStyle().Foreground(theme.Highlight).Bold(true).Render("•")
	muted := NewStyle().Foreground(theme.Border).Render("·")
	position := [4]int{0, 1, 2, 1}[frameIndex%4]
	dots := [3]string{muted, muted, muted}
	dots[position] = active
	return strings.Join(dots[:], " ")
}

func (s Style) MaxWidth(v int) Style {
	s.maxWidth = v
	return s
}

func toColorVal(c color.Color) *colorVal {
	if lc, ok := c.(colorVal); ok {
		return &lc
	}
	if c != nil {
		r, g, b, _ := c.RGBA()
		return &colorVal{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8)}
	}
	return nil
}

func parseBoxSpacing(args []int) (top, right, bottom, left int) {
	switch len(args) {
	case 1:
		return args[0], args[0], args[0], args[0]
	case 2:
		return args[0], args[1], args[0], args[1]
	case 4:
		return args[0], args[1], args[2], args[3]
	default:
		return 0, 0, 0, 0
	}
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
	var ansiStart strings.Builder
	if s.bold {
		ansiStart.WriteString("\x1b[1m")
	}
	if s.italic {
		ansiStart.WriteString("\x1b[3m")
	}
	if s.underline {
		ansiStart.WriteString("\x1b[4m")
	}
	if s.fg != nil {
		fmt.Fprintf(&ansiStart, "\x1b[38;2;%d;%d;%dm", s.fg.R, s.fg.G, s.fg.B)
	}
	if s.bg != nil {
		fmt.Fprintf(&ansiStart, "\x1b[48;2;%d;%d;%dm", s.bg.R, s.bg.G, s.bg.B)
	}
	if ansiStart.Len() == 0 {
		return "", ""
	}
	return ansiStart.String(), "\x1b[0m"
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

func (s Style) Border(b interface{}, _ ...bool) Style {
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
		contentMaxWidth -= s.paddingLeft + s.paddingRight + s.marginLeft + s.marginRight
		if contentMaxWidth < 10 {
			contentMaxWidth = 10
		}

		var wrappedLines []string
		for _, line := range strings.Split(str, "\n") {
			wrappedLines = append(wrappedLines, wrapSingleAnsiLine(line, contentMaxWidth)...)
		}
		str = strings.Join(wrappedLines, "\n")
	}

	lines := strings.Split(str, "\n")
	ansiStart, ansiEnd := s.GetSequence()

	maxWidth := 0
	for _, line := range lines {
		if w := utf8.RuneCountInString(StripAnsi(line)); w > maxWidth {
			maxWidth = w
		}
	}

	paddedWidth := maxWidth + s.paddingLeft + s.paddingRight
	padL := strings.Repeat(" ", s.paddingLeft)
	formattedLines := make([]string, len(lines))
	for i, line := range lines {
		runeCount := utf8.RuneCountInString(StripAnsi(line))
		padRCount := s.paddingRight
		if s.bg != nil || s.borderType != noBorder {
			padRCount += maxWidth - runeCount
		}
		formattedLines[i] = padL + line + strings.Repeat(" ", padRCount)
	}

	var borderedLines []string
	if s.borderType != noBorder {
		var borderStart, borderEnd string
		if s.borderColor != nil {
			borderStart = fmt.Sprintf("\x1b[38;2;%d;%d;%dm", s.borderColor.R, s.borderColor.G, s.borderColor.B)
			borderEnd = "\x1b[0m"
		}

		tl, tr, bl, br, h, v := "┌", "┐", "└", "┘", "─", "│"
		if s.borderType == roundedBorder {
			tl, tr, bl, br = "╭", "╮", "╰", "╯"
		}

		vBar := borderStart + v + borderEnd
		hBar := strings.Repeat(h, paddedWidth)
		padLine := vBar + strings.Repeat(" ", paddedWidth) + vBar

		borderedLines = make([]string, 0, len(formattedLines)+s.paddingTop+s.paddingBottom+2)
		borderedLines = append(borderedLines, borderStart+tl+hBar+tr+borderEnd)
		for i := 0; i < s.paddingTop; i++ {
			borderedLines = append(borderedLines, padLine)
		}
		for _, line := range formattedLines {
			borderedLines = append(borderedLines, vBar+ansiStart+line+ansiEnd+vBar)
		}
		for i := 0; i < s.paddingBottom; i++ {
			borderedLines = append(borderedLines, padLine)
		}
		borderedLines = append(borderedLines, borderStart+bl+hBar+br+borderEnd)
	} else {
		borderedLines = make([]string, len(formattedLines))
		for i, line := range formattedLines {
			borderedLines[i] = ansiStart + line + ansiEnd
		}
	}

	marginL := strings.Repeat(" ", s.marginLeft)
	marginR := strings.Repeat(" ", s.marginRight)
	marginLines := make([]string, 0, len(borderedLines)+s.marginTop+s.marginBottom)
	for i := 0; i < s.marginTop; i++ {
		marginLines = append(marginLines, "")
	}
	for _, line := range borderedLines {
		marginLines = append(marginLines, marginL+line+marginR)
	}
	for i := 0; i < s.marginBottom; i++ {
		marginLines = append(marginLines, "")
	}

	return strings.Join(marginLines, "\n")
}

func JoinHorizontal(_ int, strs ...string) string {
	if len(strs) == 0 {
		return ""
	}
	if len(strs) == 1 {
		return strs[0]
	}

	splitStrs := make([][]string, len(strs))
	widths := make([]int, len(strs))
	maxLines := 0

	for idx, s := range strs {
		lines := strings.Split(s, "\n")
		splitStrs[idx] = lines
		if len(lines) > maxLines {
			maxLines = len(lines)
		}
		maxW := 0
		for _, l := range lines {
			if w := utf8.RuneCountInString(StripAnsi(l)); w > maxW {
				maxW = w
			}
		}
		widths[idx] = maxW
	}

	joinedLines := make([]string, maxLines)
	for i := 0; i < maxLines; i++ {
		var lineBuilder strings.Builder
		for idx, lines := range splitStrs {
			var lineVal string
			if i < len(lines) {
				lineVal = lines[i]
			}
			visibleW := utf8.RuneCountInString(StripAnsi(lineVal))
			lineBuilder.WriteString(lineVal)
			if pad := widths[idx] - visibleW; pad > 0 {
				lineBuilder.WriteString(strings.Repeat(" ", pad))
			}
		}
		joinedLines[i] = lineBuilder.String()
	}

	return strings.Join(joinedLines, "\n")
}
