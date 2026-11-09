package agent

import (
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	filetool "loop/pkg/agent/tool/file"
)

const (
	streamPreviewRunes     = 96
	streamLivePreviewRunes = 4096
	streamPathBytes        = 512
)

// streamedDraft decodes readable argument values and keeps bounded excerpts.
// It does not require the filename to be known and never stores whole files.
type streamedDraft struct {
	toolName         string
	unicodeDigits    string
	unicodeRemaining int
	highSurrogate    rune
	inString         bool
	escaped          bool
	readingKey       bool
	readingBody      bool
	valueNext        bool
	key              strings.Builder
	lastKey          string
	readingPath      bool
	pathBuf          strings.Builder
	path             string
	edit             bool
	linePrefix       string
	lineStart        bool
	tail             []rune
	liveTail         []rune
	liveHead         int
	pending          strings.Builder
}

func (d *streamedDraft) appendVisible(r rune) {
	// Edit previews are proposals, not applied changes. Prefix both sides of
	// each replacement so a streaming edit is readable before execution.
	if d.edit && d.readingBody && d.lineStart {
		for _, prefixRune := range d.linePrefix {
			d.appendPlain(prefixRune)
		}
		d.lineStart = false
	}
	if d.edit && r == '\n' {
		d.lineStart = true
	}
	d.appendPlain(r)
}

func (d *streamedDraft) appendPlain(r rune) {
	if r == '\r' || r == '\t' {
		r = ' '
	}
	if (r < 32 && r != '\n') || r == 127 || r == '\x1b' {
		return
	}
	if len(d.liveTail) < streamLivePreviewRunes {
		d.liveTail = append(d.liveTail, r)
	} else {
		d.liveTail[d.liveHead] = r
		d.liveHead = (d.liveHead + 1) % len(d.liveTail)
	}
	d.pending.WriteRune(r)
	if r == '\n' {
		r = ' '
	}
	if len(d.tail) < streamPreviewRunes {
		d.tail = append(d.tail, r)
	} else {
		copy(d.tail, d.tail[1:])
		d.tail[len(d.tail)-1] = r
	}
}

func (d *streamedDraft) appendJSON(fragment string) {
	for _, r := range fragment {
		if d.inString {
			if d.unicodeRemaining > 0 {
				d.unicodeDigits += string(r)
				d.unicodeRemaining--
				if d.unicodeRemaining == 0 {
					value, err := strconv.ParseUint(d.unicodeDigits, 16, 16)
					if err == nil {
						d.appendUnicode(rune(value))
					}
				}
				continue
			}
			if d.escaped {
				d.escaped = false
				if r != 'u' && d.highSurrogate != 0 {
					d.appendDecoded(utf8.RuneError)
					d.highSurrogate = 0
				}
				switch r {
				case 'u':
					d.unicodeRemaining = 4
					d.unicodeDigits = ""
				case 'n':
					d.appendDecoded('\n')
				case 't':
					d.appendDecoded('\t')
				case 'r':
					d.appendDecoded('\r')
				case '"', '\\', '/':
					d.appendDecoded(r)
				}

				continue
			}
			if r == '\\' {
				d.escaped = true
				continue
			}
			if r == '"' {
				if d.highSurrogate != 0 {
					d.appendDecoded(utf8.RuneError)
					d.highSurrogate = 0
				}
				if d.toolName == "todo" && d.readingBody && d.lastKey != "markdown" {
					d.appendPlain('\n')
				}
				if d.edit && d.readingBody && !d.lineStart {
					d.appendPlain('\n')
					d.lineStart = true
				}
				d.inString = false
				if d.readingKey {
					d.lastKey = d.key.String()
					d.key.Reset()
				}
				if d.readingPath {
					d.path = d.pathBuf.String()
					d.pathBuf.Reset()
				}
				d.readingPath = false
				d.readingKey = false
				d.readingBody = false
				d.valueNext = false
				continue
			}
			if d.highSurrogate != 0 {
				d.appendDecoded(utf8.RuneError)
				d.highSurrogate = 0
			}
			if d.readingKey {
				d.key.WriteRune(r)
			} else {
				d.appendDecoded(r)
			}
			continue
		}
		switch r {
		case '"':
			d.inString = true
			key := strings.ToLower(d.lastKey)
			d.readingBody = d.valueNext && (key == "content" || key == "body" || key == "text" || key == "question" || key == "prompt")
			if d.toolName == "bash" {
				d.readingBody = d.valueNext && (key == "command" || key == "cmd" || key == "arguments")
			}
			if d.toolName == "todo" {
				d.readingBody = d.valueNext && (key == "markdown" || key == "task" || key == "description" || key == "title" || key == "content" || key == "desc" || key == "summary")
				if d.readingBody && key != "markdown" {
					for _, prefix := range "  • " {
						d.appendPlain(prefix)
					}
				}
			}
			if d.edit && d.valueNext {
				switch d.lastKey {
				case "oldText", "old_text", "old_string", "oldString":
					d.readingBody = true
					d.linePrefix = "- "
					d.lineStart = true
				case "newText", "new_text", "new_string", "newString":
					d.readingBody = true
					d.linePrefix = "+ "
					d.lineStart = true
				}
			}
			d.readingPath = d.valueNext && d.toolName != "todo" && (key == "path" || key == "file_path" || key == "filename" || key == "pattern" || key == "query" || key == "url" || key == "name" || key == "task_id")
			if d.toolName == "write" || d.toolName == "edit" {
				d.readingPath = d.valueNext && filetool.IsFilePathKey(key)
			}
			d.readingKey = !d.valueNext
		case ':':
			d.valueNext = true
		case ',':
			d.valueNext = false
			// A later content field represents a different value.
		case '{', '[':
			// A nested object or array consumes its parent's value. Without
			// resetting valueNext, keys inside edit.updates are mistaken for
			// string values and no replacement text is ever previewed.
			d.valueNext = false
			d.lastKey = ""
		}
	}
}

func (d *streamedDraft) appendUnicode(r rune) {
	if d.highSurrogate != 0 {
		high := d.highSurrogate
		d.highSurrogate = 0
		if r >= 0xdc00 && r <= 0xdfff {
			d.appendDecoded(utf16.DecodeRune(high, r))
			return
		}
		d.appendDecoded(utf8.RuneError)
	}
	if r >= 0xd800 && r <= 0xdbff {
		d.highSurrogate = r
		return
	}
	if r >= 0xdc00 && r <= 0xdfff {
		r = utf8.RuneError
	}
	d.appendDecoded(r)
}

func (d *streamedDraft) appendDecoded(r rune) {
	if d.readingKey {
		d.key.WriteRune(r)
		return
	}
	if d.readingBody {
		d.appendVisible(r)
	}
	if d.readingPath && d.pathBuf.Len() < streamPathBytes {
		d.pathBuf.WriteRune(r)
		d.path = d.pathBuf.String()
	}
}

func (d *streamedDraft) preview() string { return string(d.tail) }

// livePreview keeps a bounded tail of the generated body, including newlines.
// It is a display value only, not a tool argument or executable source.
func (d *streamedDraft) livePreview() string {
	if d.liveHead == 0 {
		return string(d.liveTail)
	}
	var b strings.Builder
	b.Grow(len(d.liveTail))
	b.WriteString(string(d.liveTail[d.liveHead:]))
	b.WriteString(string(d.liveTail[:d.liveHead]))
	return b.String()
}

// takeDelta emits each printable body character once, even when the JSON
// content value is split at arbitrary SSE chunk boundaries.
func (d *streamedDraft) takeDelta() string {
	value := d.pending.String()
	d.pending.Reset()
	return value
}
