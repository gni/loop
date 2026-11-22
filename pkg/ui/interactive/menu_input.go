package interactive

import (
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"loop/pkg/terminal"
	"loop/pkg/ui/screen"
	"loop/pkg/ui/style"
)

// Input failures leave the menu; operation failures remain visible in it.
type MenuInputError struct{ Cause error }

func (e *MenuInputError) Error() string { return e.Cause.Error() }
func (e *MenuInputError) Unwrap() error { return e.Cause }

func MenuAction(action func() error) func() error {
	return func() error {
		err := action()
		if errors.Is(err, ErrSelectionCancelled) {
			return nil
		}
		return err
	}
}

// Redraw the form on resize and keep cancellation distinct from input failures.
func ReadMenuField(sr *SessionReader, signals chan os.Signal, w io.Writer, theme style.UITheme, title, description, label, initial string, validate func(string) error) (string, error) {
	return readMenuInput(sr, signals, w, theme, title, description, label, initial, false, validate)
}

func ReadMenuSecretField(sr *SessionReader, signals chan os.Signal, w io.Writer, theme style.UITheme, title, description, label, initial string, validate func(string) error) (string, error) {
	return readMenuInput(sr, signals, w, theme, title, description, label, initial, true, validate)
}

func readMenuInput(sr *SessionReader, signals chan os.Signal, w io.Writer, theme style.UITheme, title, description, label, initial string, secret bool, validate func(string) error) (string, error) {
	var input strings.Builder
	input.WriteString(initial)
	warning := ""
	comp := screen.NewCompositor(80, 24)
	for {
		termW, termH := terminal.GetDimensions()
		if termW <= 0 {
			termW = 80
		}
		if termH <= 0 {
			termH = 24
		}
		width := max(1, termW-2)
		titleStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		muted := style.NewStyle().Foreground(theme.TextMuted)
		value := MenuDisplayText(input.String())
		if secret {
			value = strings.Repeat("•", min(width*2, utf8.RuneCountInString(input.String())))
		}
		// Long instructions remain editable without wrapping over the form controls.
		visible := []rune(value)
		start, cells := len(visible), 0
		for start > 0 {
			next := style.RuneWidth(visible[start-1])
			if cells+next > max(1, width-2) {
				break
			}
			cells += next
			start--
		}
		visible = visible[start:]
		inputLine := "> " + string(visible)
		rows := []string{
			titleStyle.Render(title),
			muted.Render(description),
			"",
			titleStyle.Render(label),
			inputLine,
			"",
			style.NewStyle().Foreground(theme.Error).Render(warning),
			"",
			muted.Render("Enter continue · Esc cancel"),
		}
		cursorRow := 5
		showCursor := true
		if termH < 10 {
			rows = []string{
				titleStyle.Render(title),
				inputLine,
				style.NewStyle().Foreground(theme.Error).Render(warning),
				muted.Render("Enter apply · Esc back"),
			}
			cursorRow = 2
		}
		if width < 16 || termH < 6 {
			rows = []string{muted.Render("Resize; Esc back")}
			cursorRow = 0
			showCursor = false
		}
		cursorCol := 0
		if showCursor {
			cursorCol = min(termW, style.CellWidth(inputLine)+1)
		}
		opts := screen.FlushOptions{
			CursorRow:  cursorRow,
			CursorCol:  cursorCol,
			ShowCursor: showCursor,
		}
		if err := FlushMenuScreen(comp, w, termW, termH, rows, opts); err != nil {
			return "", &MenuInputError{Cause: err}
		}
		key, resized, err := sr.ReadKeyOrResize(signals)
		if err != nil {
			return "", &MenuInputError{Cause: err}
		}
		if resized {
			terminal.RefreshDimensions()
			continue
		}
		if pasted, err := appendPastedInput(key, &input, io.Discard); pasted {
			if err != nil {
				return "", &MenuInputError{Cause: err}
			}
			warning = ""
			continue
		}
		switch string(key) {
		case "\x1b", "\x03", "\x04":
			return "", ErrSelectionCancelled
		case "\x15":
			input.Reset()
			warning = ""
			continue
		}
		if width < 16 || termH < 6 {
			continue
		}
		if len(key) > 0 && key[0] == 27 {
			continue
		}
		if !utf8.Valid(key) {
			continue
		}
		done, err := consumeLineBytes(key, &input, io.Discard)
		if err != nil {
			return "", ErrSelectionCancelled
		}
		warning = ""
		if !done {
			continue
		}
		value = strings.TrimSpace(input.String())
		if validate != nil {
			if err := validate(value); err != nil {
				warning = err.Error()
				continue
			}
		}
		return value, nil
	}
}
