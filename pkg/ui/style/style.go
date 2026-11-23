package style

import (
	"fmt"
	"image/color"
	"math"
	"os"
	"strings"
	"sync"
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
	Top
	Bottom
)

type colorProfile uint8

const (
	profileTrueColor colorProfile = iota
	profile256
	profileANSI16
	profileNone
)

var (
	colorProfileOnce   sync.Once
	cachedColorProfile colorProfile
)

func detectColorProfile() colorProfile {
	colorProfileOnce.Do(func() {
		if _, ok := os.LookupEnv("NO_COLOR"); ok {
			cachedColorProfile = profileNone
			return
		}
		termEnv := strings.ToLower(strings.TrimSpace(os.Getenv("TERM")))
		if termEnv == "dumb" {
			cachedColorProfile = profileNone
			return
		}
		ct := strings.ToLower(strings.TrimSpace(os.Getenv("COLORTERM")))
		if ct == "truecolor" || ct == "24bit" {
			cachedColorProfile = profileTrueColor
			return
		}
		if strings.Contains(termEnv, "truecolor") || strings.Contains(termEnv, "direct") ||
			strings.Contains(termEnv, "kitty") || strings.Contains(termEnv, "ghostty") ||
			strings.Contains(termEnv, "wezterm") || strings.Contains(termEnv, "alacritty") {
			cachedColorProfile = profileTrueColor
			return
		}
		if os.Getenv("WT_SESSION") != "" || os.Getenv("TERM_PROGRAM") != "" ||
			os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("WEZTERM_EXECUTABLE") != "" ||
			os.Getenv("GHOSTTY_RESOURCES_DIR") != "" || os.Getenv("KONSOLE_VERSION") != "" ||
			os.Getenv("VTE_VERSION") != "" || os.Getenv("ALACRITTY_SOCKET") != "" ||
			os.Getenv("ITERM_SESSION_ID") != "" || os.Getenv("TMUX") != "" {
			cachedColorProfile = profileTrueColor
			return
		}
		if strings.Contains(termEnv, "256color") {
			cachedColorProfile = profile256
			return
		}
		if strings.Contains(termEnv, "xterm") {
			cachedColorProfile = profileTrueColor
			return
		}
		if termEnv == "vt100" || termEnv == "ansi" || termEnv == "linux" {
			cachedColorProfile = profileANSI16
			return
		}
		cachedColorProfile = profileTrueColor
	})
	return cachedColorProfile
}

// ChromaFormatterName returns the Chroma terminal formatter matching the active color profile.
// Returns "" when color output is disabled (NO_COLOR or TERM=dumb).
func ChromaFormatterName() string {
	switch detectColorProfile() {
	case profileNone:
		return ""
	case profileANSI16:
		return "terminal16"
	case profile256:
		return "terminal256"
	default:
		return "terminal16m"
	}
}

var ansi256CubeLevels = [6]int{0, 95, 135, 175, 215, 255}

func nearestCubeIndex(v int) int {
	bestIdx := 0
	bestDist := 1 << 30
	for i, level := range ansi256CubeLevels {
		d := v - level
		if d < 0 {
			d = -d
		}
		if d < bestDist {
			bestDist = d
			bestIdx = i
		}
	}
	return bestIdx
}

func rgbToANSI256(r, g, b uint8) int {
	ir, ig, ib := int(r), int(g), int(b)
	ri := nearestCubeIndex(ir)
	gi := nearestCubeIndex(ig)
	bi := nearestCubeIndex(ib)
	cr, cg, cb := ansi256CubeLevels[ri], ansi256CubeLevels[gi], ansi256CubeLevels[bi]
	cubeIdx := 16 + 36*ri + 6*gi + bi
	cubeDist := (ir-cr)*(ir-cr) + (ig-cg)*(ig-cg) + (ib-cb)*(ib-cb)

	// Compare against the 24-level xterm grayscale ramp [8, 18, ..., 238].
	avg := (ir + ig + ib + 1) / 3
	grayStep := (avg - 8 + 5) / 10
	grayStep = min(23, max(0, grayStep))
	gv := 8 + grayStep*10
	grayDist := (ir-gv)*(ir-gv) + (ig-gv)*(ig-gv) + (ib-gv)*(ib-gv)
	if grayDist < cubeDist {
		return 232 + grayStep
	}
	return cubeIdx
}

func rgbToANSI16(r, g, b uint8, isBG bool) int {
	bright := r > 170 || g > 170 || b > 170
	idx := 0
	if r >= 128 {
		idx |= 1
	}
	if g >= 128 {
		idx |= 2
	}
	if b >= 128 {
		idx |= 4
	}
	if isBG {
		if bright {
			return 100 + idx
		}
		return 40 + idx
	}
	if bright {
		return 90 + idx
	}
	return 30 + idx
}

func formatColorEscape(c *colorVal, isBG bool) string {
	if c == nil {
		return ""
	}
	switch detectColorProfile() {
	case profileNone:
		return ""
	case profileANSI16:
		return fmt.Sprintf("\x1b[%dm", rgbToANSI16(c.R, c.G, c.B, isBG))
	case profile256:
		code := 38
		if isBG {
			code = 48
		}
		return fmt.Sprintf("\x1b[%d;5;%dm", code, rgbToANSI256(c.R, c.G, c.B))
	default:
		code := 38
		if isBG {
			code = 48
		}
		return fmt.Sprintf("\x1b[%d;2;%d;%d;%dm", code, c.R, c.G, c.B)
	}
}

// FormatRGBForeground returns ANSI foreground start and reset escapes matching
// the active terminal color profile (TrueColor, ANSI256, ANSI16, or empty for NO_COLOR/dumb).
func FormatRGBForeground(r, g, b uint8) (start, reset string) {
	c := colorVal{R: r, G: g, B: b}
	start = formatColorEscape(&c, false)
	if start == "" {
		return "", ""
	}
	return start, "\x1b[0m"
}

type Style struct {
	fg            *colorVal
	bg            *colorVal
	bold          bool
	italic        bool
	underline     bool
	strike        bool
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
	frames := [...]string{
		"🕛\uFE0E",
		"🕐\uFE0E",
		"🕑\uFE0E",
		"🕒\uFE0E",
		"🕓\uFE0E",
		"🕔\uFE0E",
		"🕕\uFE0E",
		"🕖\uFE0E",
		"🕗\uFE0E",
		"🕘\uFE0E",
		"🕙\uFE0E",
		"🕚\uFE0E",
	}
	if frameIndex < 0 {
		frameIndex = 0
	}

	// Calm human breathing / resting pulse gradient (28 ticks * 150ms = 4.2s full inhale/exhale cycle)
	// smoothly interpolating RGB from muted grey (TextMuted) to soft white (Text) without bold snapping.
	const breathSteps = 28
	phase := (2.0 * math.Pi * float64(frameIndex%breathSteps)) / float64(breathSteps)
	t := (1.0 - math.Cos(phase)) * 0.5

	low := toColorVal(theme.TextMuted)
	if low == nil {
		low = &colorVal{R: 102, G: 102, B: 102}
	}
	high := toColorVal(theme.Text)
	if high == nil {
		high = &colorVal{R: 224, G: 224, B: 224}
	}
	c := colorVal{
		R: uint8(float64(low.R) + t*(float64(high.R)-float64(low.R)) + 0.5),
		G: uint8(float64(low.G) + t*(float64(high.G)-float64(low.G)) + 0.5),
		B: uint8(float64(low.B) + t*(float64(high.B)-float64(low.B)) + 0.5),
	}
	return NewStyle().Foreground(c).Render(frames[frameIndex%len(frames)])
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

func (s Style) Background(c color.Color) Style {
	s.bg = toColorVal(c)
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

func (s Style) Strikethrough(v bool) Style {
	s.strike = v
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
	if s.strike {
		ansiStart.WriteString("\x1b[9m")
	}
	if fg := formatColorEscape(s.fg, false); fg != "" {
		ansiStart.WriteString(fg)
	}
	if bg := formatColorEscape(s.bg, true); bg != "" {
		ansiStart.WriteString(bg)
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

func (s Style) Border(b any, _ ...bool) Style {
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

	// Fast path for plain inline styled text with no box geometry.
	if s.maxWidth <= 0 && s.borderType == noBorder &&
		s.paddingLeft == 0 && s.paddingRight == 0 && s.paddingTop == 0 && s.paddingBottom == 0 &&
		s.marginLeft == 0 && s.marginRight == 0 && s.marginTop == 0 && s.marginBottom == 0 &&
		!strings.ContainsRune(str, '\n') {
		ansiStart, ansiEnd := s.GetSequence()
		if ansiStart == "" {
			return str
		}
		return ansiStart + str + ansiEnd
	}

	if s.maxWidth > 0 {
		contentMaxWidth := s.maxWidth
		if s.borderType != noBorder {
			contentMaxWidth -= 2
		}
		contentMaxWidth -= s.paddingLeft + s.paddingRight + s.marginLeft + s.marginRight
		if contentMaxWidth < 1 {
			contentMaxWidth = 1
		}

		var wrappedLines []string
		for line := range strings.SplitSeq(str, "\n") {
			wrappedLines = append(wrappedLines, wrapSingleAnsiLine(line, contentMaxWidth)...)
		}
		str = strings.Join(wrappedLines, "\n")
	}

	lines := strings.Split(str, "\n")
	ansiStart, ansiEnd := s.GetSequence()

	lineWidths := make([]int, len(lines))
	maxWidth := 0
	for i, line := range lines {
		w := CellWidth(line)
		lineWidths[i] = w
		if w > maxWidth {
			maxWidth = w
		}
	}

	paddedWidth := maxWidth + s.paddingLeft + s.paddingRight
	padL := strings.Repeat(" ", s.paddingLeft)
	formattedLines := make([]string, len(lines))
	for i, line := range lines {
		padRCount := s.paddingRight
		if s.bg != nil || s.borderType != noBorder {
			padRCount += maxWidth - lineWidths[i]
		}
		formattedLines[i] = padL + line + strings.Repeat(" ", padRCount)
	}

	var borderedLines []string
	if s.borderType != noBorder {
		borderStart := formatColorEscape(s.borderColor, false)
		borderEnd := ""
		if borderStart != "" {
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
			// Guard against inner ANSI escapes bleeding into the right border glyph.
			lineEnd := ansiEnd
			if lineEnd == "" && strings.ContainsRune(line, '\x1b') {
				lineEnd = "\x1b[0m"
			}
			borderedLines = append(borderedLines, vBar+ansiStart+line+lineEnd+vBar)
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

func JoinHorizontal(align int, strs ...string) string {
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
			if w := CellWidth(l); w > maxW {
				maxW = w
			}
		}
		widths[idx] = maxW
	}

	offsets := make([]int, len(strs))
	for idx, lines := range splitStrs {
		diff := maxLines - len(lines)
		switch align {
		case Bottom:
			offsets[idx] = diff
		case Center:
			offsets[idx] = diff / 2
		default:
			offsets[idx] = 0
		}
	}

	joinedLines := make([]string, maxLines)
	for i := 0; i < maxLines; i++ {
		var lineBuilder strings.Builder
		for idx, lines := range splitStrs {
			var lineVal string
			srcRow := i - offsets[idx]
			if srcRow >= 0 && srcRow < len(lines) {
				lineVal = lines[srcRow]
			}
			visibleW := CellWidth(lineVal)
			lineBuilder.WriteString(lineVal)
			if pad := widths[idx] - visibleW; pad > 0 {
				lineBuilder.WriteString(strings.Repeat(" ", pad))
			}
		}
		joinedLines[i] = lineBuilder.String()
	}

	return strings.Join(joinedLines, "\n")
}
