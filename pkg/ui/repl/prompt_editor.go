package repl

import (
	"io"
	"os"
	"unicode/utf8"

	"loop/pkg/agent"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
)

type slashMenuController interface {
	SlashMenuActive() bool
	SlashMenuTabComplete() (string, bool)
	SyncSlashMenu(draft string)
	CloseSlashMenu()
}

// PromptEditor is Loop's native interactive line editor. It reads normalized
// edit keys and UTF-8 text from KeyInterceptorReader and delegates prompt
// rendering to the shared console control plane without golang.org/x/term.Terminal.
type PromptEditor struct {
	agent    *agent.Agent
	history  *ui.CustomHistory
	reader   *interceptor.KeyInterceptorReader
	menu     slashMenuController
	redraw   func()
	draw     func()
	complete func(line string, pos int, key rune) (string, int, bool)
	pending  []byte
}

// NewPromptEditor creates a native prompt editor wired to the REPL's input
// interceptor, prompt history, and slash-command menu.
func NewPromptEditor(
	a *agent.Agent,
	history *ui.CustomHistory,
	reader *interceptor.KeyInterceptorReader,
	menu slashMenuController,
) *PromptEditor {
	e := &PromptEditor{
		agent:   a,
		history: history,
		reader:  reader,
		menu:    menu,
	}
	e.complete = func(line string, pos int, key rune) (string, int, bool) {
		return autoCompleteCallback(line, pos, key, a)
	}
	return e
}

// ReadLine edits a single prompt until Enter submits it, Ctrl+D signals EOF on
// an empty line, or the underlying reader returns an error.
func (e *PromptEditor) ReadLine() (string, error) {
	if e == nil || e.reader == nil {
		return "", io.EOF
	}

	line := ""
	pos := 0
	pending := e.pending
	e.pending = nil
	needMore := false
	buf := make([]byte, 4096)

	for {
		var readErr error
		if len(pending) == 0 || needMore {
			needMore = false
			n, err := e.reader.Read(buf)
			if n > 0 {
				pending = append(pending, buf[:n]...)
			}
			readErr = err
		}

		if len(pending) > 0 {
			dirty := false
			didRedrawScreen := false

			for len(pending) > 0 {
				b := pending[0]
				switch b {
				case '\r', '\n':
					if b == '\r' && len(pending) > 1 && pending[1] == '\n' {
						pending = pending[2:]
					} else {
						pending = pending[1:]
					}
					if len(pending) > 0 {
						e.pending = append([]byte(nil), pending...)
					}
					return line, nil
				case '\x04':
					pending = pending[1:]
					if len(line) == 0 {
						if len(pending) > 0 {
							e.pending = append([]byte(nil), pending...)
						}
						return "", io.EOF
					}
					var changed bool
					line, pos, changed = e.applyEditKey(line, pos, interceptor.PromptDeleteKey)
					if changed {
						dirty = true
					}
					continue
				case '\x0c':
					pending = pending[1:]
					pos = min(max(0, pos), len(line))
					e.reader.SetPromptDraft(line, utf8.RuneCountInString(line[:pos]))
					e.triggerRedraw()
					didRedrawScreen = true
					dirty = false
					continue
				case '\x1b':
					consumed, bareEsc := consumeEscapeSequence(pending)
					pending = pending[consumed:]
					if bareEsc && e.menu != nil && e.menu.SlashMenuActive() {
						e.menu.CloseSlashMenu()
						dirty = true
					}
					continue
				case '\t':
					pending = pending[1:]
					var changed bool
					line, pos, changed = e.applyTab(line, pos)
					if changed {
						dirty = true
					}
					continue
				}

				if !utf8.FullRune(pending) && len(pending) < utf8.UTFMax {
					needMore = true
					break
				}
				r, size := utf8.DecodeRune(pending)
				pending = pending[size:]
				if r == utf8.RuneError && size == 1 {
					continue
				}
				var handled bool
				line, pos, handled = e.applyEditKey(line, pos, r)
				if handled {
					dirty = true
				}
			}

			if dirty && !didRedrawScreen {
				e.triggerDraw()
			}
		}

		if readErr != nil {
			return line, readErr
		}
	}
}

func (e *PromptEditor) applyTab(line string, pos int) (string, int, bool) {
	pos = min(max(0, pos), len(line))
	e.reader.SetPromptDraft(line, utf8.RuneCountInString(line[:pos]))
	if e.menu != nil && e.menu.SlashMenuActive() {
		if fill, ok := e.menu.SlashMenuTabComplete(); ok && fill != line {
			pos = len(fill)
			e.reader.SetPromptDraft(fill, utf8.RuneCountInString(fill))
			e.menu.SyncSlashMenu(fill)
			return fill, pos, true
		}
	}
	if e.complete == nil {
		return line, pos, false
	}
	newLine, newPos, completed := e.complete(line, pos, '\t')
	if !completed {
		return line, pos, false
	}
	newPos = min(max(0, newPos), len(newLine))
	lineChanged := newLine != line
	changed := lineChanged || newPos != pos
	e.reader.SetPromptDraft(newLine, utf8.RuneCountInString(newLine[:newPos]))
	if lineChanged && e.menu != nil {
		e.menu.SyncSlashMenu(newLine)
	}
	return newLine, newPos, changed
}

func (e *PromptEditor) applyEditKey(line string, pos int, key rune) (string, int, bool) {
	pos = min(max(0, pos), len(line))
	e.reader.SetPromptDraft(line, utf8.RuneCountInString(line[:pos]))
	newLine, newPos, handled := handlePromptEditKey(line, pos, key, e.agent, e.history, e.reader)
	if !handled {
		return line, pos, false
	}
	newPos = min(max(0, newPos), len(newLine))
	e.reader.SetPromptDraft(newLine, utf8.RuneCountInString(newLine[:newPos]))
	if newLine != line && e.menu != nil {
		if key == interceptor.PromptUpKey || key == interceptor.PromptDownKey || key == interceptor.PromptHistoryUpKey {
			e.menu.CloseSlashMenu()
		} else {
			e.menu.SyncSlashMenu(newLine)
		}
	}
	return newLine, newPos, true
}

func (e *PromptEditor) triggerDraw() {
	if e.draw != nil {
		e.draw()
		return
	}
	if e.reader != nil && e.reader.OnDrawStaticControls != nil {
		w := e.reader.W
		if w == nil {
			w = os.Stderr
		}
		e.reader.OnDrawStaticControls(w)
	}
}

func (e *PromptEditor) triggerRedraw() {
	if e.redraw != nil {
		e.redraw()
		return
	}
	if e.reader != nil && e.reader.OnRedrawScreen != nil {
		e.reader.OnRedrawScreen()
	}
}

// consumeEscapeSequence returns the byte count of an unhandled escape sequence
// starting at data[0] == 0x1b, and reports whether it was a bare Escape key.
func consumeEscapeSequence(data []byte) (int, bool) {
	if len(data) <= 1 {
		return len(data), true
	}
	switch data[1] {
	case '[':
		if len(data) == 2 {
			return 2, false
		}
		if data[2] == 'M' {
			return min(len(data), 6), false
		}
		if data[2] == '[' {
			return min(len(data), 4), false
		}
		for i := 2; i < len(data); i++ {
			if data[i] >= 0x40 && data[i] <= 0x7e {
				return i + 1, false
			}
		}
		return len(data), false
	case 'O':
		return min(len(data), 3), false
	case ']', 'P', 'X', '^', '_':
		for i := 2; i < len(data); i++ {
			if data[1] == ']' && data[i] == '\a' {
				return i + 1, false
			}
			if data[i] == '\x1b' && i+1 < len(data) && data[i+1] == '\\' {
				return i + 2, false
			}
		}
		return len(data), false
	default:
		return 2, false
	}
}
