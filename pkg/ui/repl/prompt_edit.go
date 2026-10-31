package repl

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"loop/pkg/agent"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func handlePromptEditKey(line string, pos int, key rune, a *agent.Agent, history *ui.CustomHistory, reader *interceptor.KeyInterceptorReader) (string, int, bool) {
	pos = min(max(0, pos), len(line))
	switch key {
	case interceptor.PromptPasteKey:
		text := reader.TakePromptPaste()
		reader.HistoryIndex = -1
		return line[:pos] + text + line[pos:], pos + len(text), true
	case interceptor.PromptNewlineKey:
		reader.HistoryIndex = -1
		return line[:pos] + "\n" + line[pos:], pos + 1, true
	case interceptor.PromptHomeKey:
		return line, strings.LastIndex(line[:pos], "\n") + 1, true
	case interceptor.PromptEndKey:
		if end := strings.IndexByte(line[pos:], '\n'); end >= 0 {
			return line, pos + end, true
		}
		return line, len(line), true
	case interceptor.PromptWordLeftKey, interceptor.PromptWordRightKey:
		runes := []rune(line)
		index := utf8.RuneCountInString(line[:pos])
		if key == interceptor.PromptWordLeftKey {
			for index > 0 && unicode.IsSpace(runes[index-1]) {
				index--
			}
			for index > 0 && !unicode.IsSpace(runes[index-1]) {
				index--
			}
		} else {
			for index < len(runes) && !unicode.IsSpace(runes[index]) {
				index++
			}
			for index < len(runes) && unicode.IsSpace(runes[index]) {
				index++
			}
		}
		return line, len(string(runes[:index])), true
	case interceptor.PromptUpKey, interceptor.PromptDownKey, interceptor.PromptHistoryUpKey:
		direction := -1
		if key == interceptor.PromptDownKey {
			direction = 1
		}
		if key == interceptor.PromptUpKey || key == interceptor.PromptDownKey {
			width, _ := ui.GetTerminalSize()
			prefix := ui.GetPromptSymbol(a.Config)
			if reader.MAM != nil {
				if name := reader.MAM.ActiveAgentName(); name != "" {
					prefix = "[" + name + "]" + prefix
				}
			}
			_, fullPrefix := style.FormatPromptWithQueue(ui.GetConfiguredTheme(a.Config), prefix, reader.QueueLen())
			columns := max(1, width-utf8.RuneCountInString(fullPrefix)-1)
			runePos := utf8.RuneCountInString(line[:pos])
			if next, ok := render.MovePromptVertical(line, runePos, columns, direction); ok {
				return line, len(string([]rune(line)[:next])), true
			}
			// Keep draft navigation in the editor. A recalled history entry
			// can advance to the newer entry with Down at its last row.
			if len(render.LayoutPrompt(line, runePos, columns).Rows) > 1 && !(direction > 0 && reader.HistoryIndex >= 0) {
				return line, pos, true
			}
		}
		if direction < 0 {
			if reader.HistoryIndex+1 < history.Len() {
				if reader.HistoryIndex < 0 {
					reader.SavedTypeAhead = []byte(line)
				}
				reader.HistoryIndex++
				line = history.GetFull(history.At(reader.HistoryIndex))
				pos = len(line)
			}
		} else if reader.HistoryIndex >= 0 {
			reader.HistoryIndex--
			if reader.HistoryIndex < 0 {
				line = string(reader.SavedTypeAhead)
			} else {
				line = history.GetFull(history.At(reader.HistoryIndex))
			}
			pos = len(line)
		}
		return line, pos, true
	}
	// x/term stops accepting printable keys at 4096 runes. Large drafts use
	// the same bulk-edit path so users can keep editing after a large paste.
	if unicode.IsPrint(key) && utf8.RuneCountInString(line) >= 4096 {
		text := string(key)
		return line[:pos] + text + line[pos:], pos + len(text), true
	}
	return line, pos, false
}
