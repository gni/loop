package ui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"loop/pkg/ui/style"
)

func visibleWidth(s string) int {
	return utf8.RuneCountInString(style.StripAnsi(s))
}

// StartResumeLoadingIndicator displays a centered HUD card featuring a simple animated cat
// and status telemetry in the center of the terminal.
func StartResumeLoadingIndicator(w io.Writer, theme UITheme, sessionID string, isResume ...bool) func() {
	if f, ok := w.(*os.File); ok {
		if !term.IsTerminal(int(f.Fd())) {
			return func() {}
		}
	}

	resuming := true
	if len(isResume) > 0 {
		resuming = isResume[0]
	}
	if !resuming {
		return func() {}
	}

	stop := make(chan struct{})
	done := make(chan struct{})

	shortID := sessionID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	if shortID == "" {
		if resuming {
			shortID = "active"
		} else {
			shortID = "new"
		}
	}

	type catFrame struct {
		ear  string
		face string
		tail string
		paws string
	}

	catFrames := []catFrame{
		{ear: "  /\\_/\\  ", face: " (=o.o=)", tail: "  ", paws: "  > ^ <  "},
		{ear: "  /\\_/\\  ", face: " (=o.o=)", tail: "~ ", paws: "  > ^ <  "},
		{ear: "  /\\_/\\  ", face: " (=o.o=)", tail: "  ", paws: "  > ^ <  "},
		{ear: "  /\\_/\\  ", face: " (=-.-=)", tail: "_ ", paws: "  > ^ <  "},
		{ear: "  /\\_/\\  ", face: " (=o.o=)", tail: "  ", paws: "  > ^ <  "},
		{ear: "  /\\_/\\  ", face: " (=o.o=)", tail: "~ ", paws: "  > ^ <  "},
		{ear: "  /\\_/\\  ", face: " (=o.o=)", tail: "_ ", paws: "  > ^ <  "},
		{ear: "  /\\_/\\  ", face: " (=-.-=)", tail: "  ", paws: "  > ^ <  "},
	}

	compactPrisms := []string{
		"◇",
		"◈",
		"◆",
		"⟡",
		"◆",
		"◈",
	}

	pulseGlyphs := []string{"◈", "◇", "◆", "◇"}

	borderStyle := style.NewStyle().Foreground(theme.Border)
	titleStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	idStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
	catStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
	tailStyle := style.NewStyle().Foreground(theme.Success).Bold(true)
	subStyle := style.NewStyle().Foreground(theme.TextMuted).Italic(true)
	dimDotStyle := style.NewStyle().Foreground(theme.Border)

	headerText := "RESUMING SESSION"
	activityText := "restoring context graph..."
	statusSub := fmt.Sprintf("indexing turns %s verifying replay %s ready", dimDotStyle.Render("·"), dimDotStyle.Render("·"))
	actionVerb := "resuming"

	var (
		lastStartY int
		lastStartX int
		lastCardW  int
		lastCardH  int
		renderMu   sync.Mutex
	)

	renderFrame := func(frame int) {
		renderMu.Lock()
		defer renderMu.Unlock()

		termW, termH := GetTerminalSize()
		if termW <= 0 {
			termW = 80
		}
		if termH <= 0 {
			termH = 24
		}

		pulse := pulseGlyphs[frame%len(pulseGlyphs)]

		var rows []string
		var cardW, cardH, startY, startX int

		if termW >= 54 && termH >= 8 {
			cardW = 54
			cardH = 6
			startY = max(1, (termH-cardH)/2)
			startX = max(1, (termW-cardW)/2)

			innerW := cardW - 2
			r1 := borderStyle.Render("╭" + strings.Repeat("─", innerW) + "╮")

			// Row 2: Title and session ID
			titleLeft := fmt.Sprintf(" %s  %s", pulse, titleStyle.Render(headerText))
			titleRight := fmt.Sprintf("[%s] ", idStyle.Render(shortID))
			titlePad := max(0, innerW-visibleWidth(titleLeft)-visibleWidth(titleRight))
			r2 := borderStyle.Render("│") + titleLeft + strings.Repeat(" ", titlePad) + titleRight + borderStyle.Render("│")

			// Cat frames
			cf := catFrames[frame%len(catFrames)]

			// Row 3: Cat ears
			catEar := catStyle.Render(cf.ear)
			catPad1 := max(0, innerW-visibleWidth(catEar))
			r3 := borderStyle.Render("│") + catEar + strings.Repeat(" ", catPad1) + borderStyle.Render("│")

			// Row 4: Cat face + tail + activity text
			catFaceWithTail := catStyle.Render(cf.face) + tailStyle.Render(cf.tail)
			row4Left := fmt.Sprintf(" %s   %s", catFaceWithTail, subStyle.Render(activityText))
			catPad2 := max(0, innerW-visibleWidth(row4Left))
			r4 := borderStyle.Render("│") + row4Left + strings.Repeat(" ", catPad2) + borderStyle.Render("│")

			// Row 5: Cat paws + sub status
			catPaws := catStyle.Render(cf.paws)
			row5Left := fmt.Sprintf(" %s    %s", catPaws, subStyle.Render(statusSub))
			catPad3 := max(0, innerW-visibleWidth(row5Left))
			r5 := borderStyle.Render("│") + row5Left + strings.Repeat(" ", catPad3) + borderStyle.Render("│")

			r6 := borderStyle.Render("╰" + strings.Repeat("─", innerW) + "╯")

			rows = []string{r1, r2, r3, r4, r5, r6}
		} else {
			// Compact single-line indicator for compact / split-pane terminals
			cardH = 1
			startY = max(1, termH/2)
			prism := compactPrisms[frame%len(compactPrisms)]
			compactText := fmt.Sprintf("%s %s %s %s...",
				pulse,
				catStyle.Render(prism),
				actionVerb,
				idStyle.Render(shortID),
			)
			cardW = visibleWidth(compactText)
			if cardW >= termW-2 && termW > 10 {
				compactText = fmt.Sprintf("%s %s %s...", catStyle.Render(prism), actionVerb, idStyle.Render(shortID))
				cardW = visibleWidth(compactText)
			}
			startX = max(1, (termW-cardW)/2)
			rows = []string{compactText}
		}

		lastStartY = startY
		lastStartX = startX
		lastCardW = cardW
		lastCardH = cardH

		var buf bytes.Buffer
		buf.WriteString("\x1b7\x1b[?25l")
		for i, r := range rows {
			fmt.Fprintf(&buf, "\x1b[%d;%dH%s", startY+i, startX, r)
		}
		buf.WriteString("\x1b8")
		TerminalMu.Lock()
		_, _ = w.Write(buf.Bytes())
		TerminalMu.Unlock()
	}

	var stopOnce sync.Once
	var stopFunc func()

	stopFunc = func() {
		stopOnce.Do(func() {
			close(stop)
			<-done

			renderMu.Lock()
			defer renderMu.Unlock()

			var buf bytes.Buffer
			buf.WriteString("\x1b7")
			emptyLine := strings.Repeat(" ", max(1, lastCardW))
			for i := 0; i < lastCardH; i++ {
				fmt.Fprintf(&buf, "\x1b[%d;%dH%s", lastStartY+i, lastStartX, emptyLine)
			}
			buf.WriteString("\x1b8\x1b[?25h")

			TerminalMu.Lock()
			_, _ = w.Write(buf.Bytes())
			if h := getUI().ChatHistory; h != nil {
				h.drawn = nil
				h.paintLocked()
			}
			TerminalMu.Unlock()
		})
	}

	renderFrame(0)

	go func() {
		defer close(done)
		ticker := time.NewTicker(75 * time.Millisecond)
		defer ticker.Stop()
		frame := 1
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				renderFrame(frame)
				frame++
			}
		}
	}()

	return stopFunc
}
