package interceptor

import (
	"bytes"
	"fmt"
	"strings"
	"time"
)

const pasteDrainTimeout = 50 * time.Millisecond

var (
	bracketedPasteStart = []byte("\x1b[200~")
	bracketedPasteEnd   = []byte("\x1b[201~")
)

func drainPasteBytes(ch <-chan byte, buf []byte, stop func([]byte) bool) []byte {
	if ch == nil {
		return buf
	}
	timer := time.NewTimer(pasteDrainTimeout)
	defer timer.Stop()

	for stop == nil || !stop(buf) {
		select {
		case b, ok := <-ch:
			if !ok {
				return buf
			}
			buf = append(buf, b)
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(pasteDrainTimeout)
		case <-timer.C:
			return buf
		}
	}
	return buf
}

func (ki *KeyInterceptorReader) handlePasteData(p []byte, n int) (int, bool) {
	data := p[:n]
	if len(ki.PasteStartPending) > 0 {
		data = append(ki.PasteStartPending, data...)
		ki.PasteStartPending = nil
	}
	var prefix []byte
	if !ki.InBracketedPaste {
		if start := bytes.Index(data, bracketedPasteStart); start >= 0 {
			prefix = append([]byte(nil), data[:start]...)
			data = data[start+len(bracketedPasteStart):]
			ki.InBracketedPaste = true
		} else {
			// A read can stop in the middle of the opening marker too.
			for size := min(len(data), len(bracketedPasteStart)-1); size > 0; size-- {
				if size == 1 && len(data) < len(p) {
					// A standalone Escape has already been disambiguated by
					// the keyboard reader. Keep it usable for menu dismissal.
					continue
				}
				if bytes.Equal(data[len(data)-size:], bracketedPasteStart[:size]) {
					ki.PasteStartPending = append([]byte(nil), data[len(data)-size:]...)
					data = data[:len(data)-size]
					break
				}
			}
			trimmed := bytes.TrimRight(data, "\r\n")
			if len(trimmed) > 0 && bytes.ContainsAny(trimmed, "\r\n") {
				pasted := drainPasteBytes(ki.InputChan, append([]byte(nil), data...), nil)
				return ki.copyReadData(p, ki.pastedInput(pasted)), true
			}
			if len(data) == 0 && len(ki.PasteStartPending) > 0 {
				return 0, true
			}
			return ki.copyReadData(p, NormalizePromptNavigationKeys(data)), false
		}
	}
	searchFrom := max(0, len(ki.BracketedBuffer)-len(bracketedPasteEnd)+1)
	ki.BracketedBuffer = append(ki.BracketedBuffer, data...)
	end := bytes.Index(ki.BracketedBuffer[searchFrom:], bracketedPasteEnd)
	if end < 0 {
		// Keep incomplete markers and UTF-8 sequences until the frame is complete.
		return ki.copyReadData(p, ki.normalizePromptEditKeys(NormalizePromptNavigationKeys(prefix))), true
	}
	end += searchFrom
	input := append(ki.normalizePromptEditKeys(NormalizePromptNavigationKeys(prefix)), ki.pastedInput(ki.BracketedBuffer[:end])...)
	remaining := ki.BracketedBuffer[end+len(bracketedPasteEnd):]
	if len(remaining) > 0 {
		ki.InputRemaining = append(append([]byte(nil), remaining...), ki.InputRemaining...)
	}
	ki.BracketedBuffer = nil
	ki.InBracketedPaste = false
	return ki.copyReadData(p, input), true
}

func NormalizePastedText(data []byte) string {
	return strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n")
}

func (ki *KeyInterceptorReader) pastedInput(data []byte) []byte {
	text := NormalizePastedText(data)
	if !ki.IsAtMainPrompt.Load() {
		// Settings fields consume literal text rather than prompt edit keys.
		return []byte(text)
	}
	text = ki.preparePastedText(text)
	ki.PastedLiterals = append(ki.PastedLiterals, text)
	return ki.QueuePromptPaste(text)
}

// PastedInputSource excludes clipboard contents from command/attachment syntax
// while retaining explicit file references typed around an unchanged paste.
func PastedInputSource(line string, pastes []string) (string, bool) {
	if len(pastes) == 0 {
		return line, false
	}
	source := line
	seen := make(map[string]bool)
	for _, text := range pastes {
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		if !strings.Contains(source, text) {
			// Edits can invalidate a paste's original boundaries. Keep the
			// input literal rather than interpreting examples as file paths.
			return "", true
		}
		source = strings.ReplaceAll(source, text, "[paste]")
	}
	return source, true
}

// QueuePromptPaste inserts one edit rather than replaying every pasted character
// through x/term's limited single-line input path. Only the editor goroutine
// queues and consumes these edits.
func (ki *KeyInterceptorReader) QueuePromptPaste(text string) []byte {
	ki.PendingPastes = append(ki.PendingPastes, text)
	return []byte(string(PromptPasteKey))
}

func (ki *KeyInterceptorReader) TakePromptPaste() string {
	if len(ki.PendingPastes) == 0 {
		return ""
	}
	text := ki.PendingPastes[0]
	ki.PendingPastes[0] = ""
	ki.PendingPastes = ki.PendingPastes[1:]
	return text
}

func (ki *KeyInterceptorReader) preparePastedText(pasteStr string) string {
	lineCount := strings.Count(pasteStr, "\n")
	if len(pasteStr) > 0 && !strings.HasSuffix(pasteStr, "\n") {
		lineCount++
	}

	maxPasteLines := 80
	maxPasteChars := 8000
	if ki.Agent != nil && ki.Agent.Config != nil {
		if ki.Agent.Config.MaxPasteLines > 0 {
			maxPasteLines = ki.Agent.Config.MaxPasteLines
		}
		if ki.Agent.Config.MaxPasteChars > 0 {
			maxPasteChars = ki.Agent.Config.MaxPasteChars
		}
	}

	if lineCount > maxPasteLines || len(pasteStr) > maxPasteChars {
		if ki.PastedCodeBlocks == nil {
			ki.PastedCodeBlocks = make(map[string]string)
		}
		ki.PasteCounter++
		unit, count := "chars", len(pasteStr)
		if lineCount > 1 {
			unit, count = "lines", lineCount
		}
		tagStr := fmt.Sprintf("[Pasted %d %s]", count, unit)
		if _, exists := ki.PastedCodeBlocks[tagStr]; exists {
			tagStr = fmt.Sprintf("[Pasted %d %s #%d]", count, unit, ki.PasteCounter)
		}
		ki.PastedCodeBlocks[tagStr] = pasteStr
		return tagStr
	}
	return pasteStr
}
