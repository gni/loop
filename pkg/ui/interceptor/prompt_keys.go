package interceptor

import (
	"strings"
)

// Private keys reach x/term's edit callback without invoking its Enter or
// history actions. They are consumed by the callback and never saved as text.
const (
	PromptNewlineKey rune = '\U000f0000' + iota
	PromptPasteKey
	PromptUpKey
	PromptDownKey
	PromptHomeKey
	PromptEndKey
	PromptHistoryUpKey
	PromptWordLeftKey
	PromptWordRightKey
)

var promptEditKeys = strings.NewReplacer(
	"\x1b[A", string(PromptUpKey), "\x1bOA", string(PromptUpKey),
	"\x1b[B", string(PromptDownKey), "\x1bOB", string(PromptDownKey),
	"\x1b[H", string(PromptHomeKey), "\x1bOH", string(PromptHomeKey), "\x1b[1~", string(PromptHomeKey),
	"\x1b[F", string(PromptEndKey), "\x1bOF", string(PromptEndKey), "\x1b[4~", string(PromptEndKey),
	"\x1b[1;3D", string(PromptWordLeftKey), "\x1b[1;3C", string(PromptWordRightKey),
	"\x10", string(PromptHistoryUpKey), "\x0e", string(PromptNewlineKey),
	"\x01", string(PromptHomeKey), "\x05", string(PromptEndKey),
)

func (ki *KeyInterceptorReader) normalizePromptEditKeys(input []byte) []byte {
	if !ki.IsAtMainPrompt.Load() {
		return input
	}
	text := promptEditKeys.Replace(string(input))
	newline := string(PromptNewlineKey)
	text = strings.ReplaceAll(text, "\x1e", newline)
	text = strings.ReplaceAll(text, "\n", newline) // Ctrl+J is the portable newline shortcut.
	return []byte(text)
}
