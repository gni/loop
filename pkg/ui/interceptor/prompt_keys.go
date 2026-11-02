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
	PromptLeftKey
	PromptRightKey
	PromptBackspaceKey
	PromptDeleteKey
	PromptDeleteWordKey
	PromptKillToEndKey
	PromptKillToStartKey
	PromptSentinelKey
)

var promptEditKeys = strings.NewReplacer(
	"\x1b[A", string(PromptUpKey), "\x1bOA", string(PromptUpKey),
	"\x1b[B", string(PromptDownKey), "\x1bOB", string(PromptDownKey),
	"\x1b[C", string(PromptRightKey), "\x1bOC", string(PromptRightKey),
	"\x1b[D", string(PromptLeftKey), "\x1bOD", string(PromptLeftKey),
	"\x1b[H", string(PromptHomeKey), "\x1bOH", string(PromptHomeKey), "\x1b[1~", string(PromptHomeKey),
	"\x1b[F", string(PromptEndKey), "\x1bOF", string(PromptEndKey), "\x1b[4~", string(PromptEndKey),
	"\x1b[3~", string(PromptDeleteKey),
	"\x1b[1;3D", string(PromptWordLeftKey), "\x1b[1;3C", string(PromptWordRightKey),
	"\x10", string(PromptHistoryUpKey), "\x0e", string(PromptNewlineKey),
	"\x01", string(PromptHomeKey), "\x05", string(PromptEndKey),
	"\x02", string(PromptLeftKey), "\x06", string(PromptRightKey),
	"\x7f", string(PromptBackspaceKey), "\x08", string(PromptBackspaceKey),
	"\x17", string(PromptDeleteWordKey),
	"\x0b", string(PromptKillToEndKey),
	"\x15", string(PromptKillToStartKey),
)

func (ki *KeyInterceptorReader) normalizePromptEditKeys(input []byte) []byte {
	if !ki.IsAtMainPrompt.Load() {
		return input
	}
	if len(input) == 1 && input[0] == 4 && ki.HasPromptDraft() {
		return []byte(string(PromptDeleteKey))
	}
	text := promptEditKeys.Replace(string(input))
	newline := string(PromptNewlineKey)
	text = strings.ReplaceAll(text, "\x1e", newline)
	text = strings.ReplaceAll(text, "\n", newline) // Ctrl+J is the portable newline shortcut.
	return []byte(text)
}
