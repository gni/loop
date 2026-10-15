package interceptor

import (
	"bytes"
	"fmt"
	"strings"
	"time"
)

func (ki *KeyInterceptorReader) handlePasteData(p []byte, n int) (int, bool) {
	var isPaste bool
	var pasteBytes []byte

	if bytes.Contains(p[:n], []byte("\x1b[200~")) || ki.InBracketedPaste {
		isPaste = true
		ki.InBracketedPaste = true
		ki.BracketedBuffer = append(ki.BracketedBuffer, p[:n]...)

		if ki.InputChan != nil {
			for !bytes.Contains(ki.BracketedBuffer, []byte("\x1b[201~")) {
				select {
				case b, ok := <-ki.InputChan:
					if !ok {
						goto bracketedDrainDone
					}
					ki.BracketedBuffer = append(ki.BracketedBuffer, b)
				case <-time.After(50 * time.Millisecond):
					goto bracketedDrainDone
				}
			}
		}
	bracketedDrainDone:
		if startIdx := bytes.Index(ki.BracketedBuffer, []byte("\x1b[200~")); startIdx >= 0 {
			ki.BracketedBuffer = ki.BracketedBuffer[startIdx+6:]
		}
		if endIdx := bytes.Index(ki.BracketedBuffer, []byte("\x1b[201~")); endIdx >= 0 {
			pasteBytes = ki.BracketedBuffer[:endIdx]
			remainder := ki.BracketedBuffer[endIdx+6:]
			ki.BracketedBuffer = nil
			ki.InBracketedPaste = false
			if len(remainder) > 0 {
				ki.PasteRemaining = append(ki.PasteRemaining, remainder...)
			}
		} else {
			pasteBytes = ki.BracketedBuffer
			ki.BracketedBuffer = nil
		}
	} else if n > 1 {
		trimmed := bytes.TrimRight(p[:n], "\r\n")
		if len(trimmed) > 0 && bytes.ContainsAny(trimmed, "\r\n") {
			isPaste = true
			pasteBytes = append([]byte(nil), p[:n]...)
			if ki.InputChan != nil {
				for {
					select {
					case b, ok := <-ki.InputChan:
						if !ok {
							goto rawPasteDrainDone
						}
						pasteBytes = append(pasteBytes, b)
					case <-time.After(50 * time.Millisecond):
						goto rawPasteDrainDone
					}
				}
			}
		rawPasteDrainDone:
		}
	}

	if isPaste {
		return ki.formatPastedText(pasteBytes, p)
	}

	return ki.copyReadData(p, NormalizePromptNavigationKeys(p[:n])), false
}

func (ki *KeyInterceptorReader) formatPastedText(pasteBytes []byte, p []byte) (int, bool) {
	pasteStr := strings.ReplaceAll(strings.ReplaceAll(string(pasteBytes), "\r\n", "\n"), "\r", "\n")

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
		return ki.copyReadData(p, []byte(tagStr)), true
	}

	singleLine := strings.ReplaceAll(pasteStr, "\n", " ↵ ")
	return ki.copyReadData(p, []byte(singleLine)), true
}