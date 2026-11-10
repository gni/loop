package interactive

import (
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

// Input failures leave the menu; operation failures remain visible in it.
type menuInputError struct{ cause error }

func (e *menuInputError) Error() string { return e.cause.Error() }
func (e *menuInputError) Unwrap() error { return e.cause }

func menuAction(action func() error) func() error {
	return func() error {
		err := action()
		if errors.Is(err, ErrSelectionCancelled) {
			return nil
		}
		return err
	}
}

// Redraw the form on resize and keep cancellation distinct from input failures.
func readMenuField(sr *SessionReader, signals chan os.Signal, w io.Writer, theme style.UITheme, title, description, label, initial string, validate func(string) error) (string, error) {
	return readMenuInput(sr, signals, w, theme, title, description, label, initial, false, validate)
}

func readMenuSecretField(sr *SessionReader, signals chan os.Signal, w io.Writer, theme style.UITheme, title, description, label, initial string, validate func(string) error) (string, error) {
	return readMenuInput(sr, signals, w, theme, title, description, label, initial, true, validate)
}

func readMenuInput(sr *SessionReader, signals chan os.Signal, w io.Writer, theme style.UITheme, title, description, label, initial string, secret bool, validate func(string) error) (string, error) {
	var input strings.Builder
	input.WriteString(initial)
	warning := ""
	for {
		width, height := terminal.GetDimensions()
		width = max(1, width-2)
		titleStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		muted := style.NewStyle().Foreground(theme.TextMuted)
		value := menuDisplayText(input.String())
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
		rows := []string{titleStyle.Render(title), muted.Render(description), "", titleStyle.Render(label), "> " + string(visible), "", style.NewStyle().Foreground(theme.Error).Render(warning), "", muted.Render("Enter continue · Esc cancel")}
		if height < 10 {
			rows = []string{titleStyle.Render(title), "> " + string(visible), style.NewStyle().Foreground(theme.Error).Render(warning), muted.Render("Enter apply · Esc back")}
		}
		if width < 16 || height < 6 {
			rows = []string{muted.Render("Resize; Esc back")}
		}
		var frame strings.Builder
		frame.WriteString("\x1b[?25l\x1b[H")
		for i, row := range rows {
			if i > 0 {
				frame.WriteString("\r\n")
			}
			frame.WriteString(style.TruncateCells(row, width) + "\x1b[K")
		}
		frame.WriteString("\x1b[J")
		if err := writeMenuFrame(w, frame.String()); err != nil {
			return "", &menuInputError{cause: err}
		}
		key, resized, err := sr.ReadKeyOrResize(signals)
		if err != nil {
			return "", &menuInputError{cause: err}
		}
		if resized {
			continue
		}
		if pasted, err := appendPastedInput(key, &input, io.Discard); pasted {
			if err != nil {
				return "", &menuInputError{cause: err}
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
		if width < 16 || height < 6 {
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
