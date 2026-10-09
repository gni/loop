package agent

import "strings"

const maxFallbackOpeningBytes = 4096

var fallbackOpeningMarkers = [...]string{
	"<tool_call",
	"<execute",
	"<tool:",
	"<function",
}

// fallbackToolTextFilter removes fallback XML tool invocations before assistant
// text reaches the terminal or conversation content. It retains only short
// delimiter prefixes across chunks, so tool payload size does not grow its
// internal buffer.
type fallbackToolTextFilter struct {
	pending                  string
	closingTag               string
	discardLeadingWhitespace bool
	emit                     func(string)
	flushed                  bool
	emitToolName             func(string, int)
	emitToolCall             func(string, int)
	toolCallIndex            int
	activeToolName           string
}

func newFallbackToolTextFilter(emit func(string)) *fallbackToolTextFilter {
	return &fallbackToolTextFilter{emit: emit}
}

func (f *fallbackToolTextFilter) SetToolCallbacks(emitToolName func(string, int), emitToolCall func(string, int)) {
	if f == nil {
		return
	}
	f.emitToolName = emitToolName
	f.emitToolCall = emitToolCall
}

func (f *fallbackToolTextFilter) Write(chunk string) {
	if f == nil || f.flushed || chunk == "" {
		return
	}
	f.pending += chunk
	f.process(false)
}

func (f *fallbackToolTextFilter) Flush() {
	if f == nil || f.flushed {
		return
	}
	f.flushed = true
	f.process(true)
}

func extractFallbackToolName(opening string) string {
	for _, attr := range []string{`name="`, `name='`} {
		if idx := strings.Index(opening, attr); idx >= 0 {
			rest := opening[idx+len(attr):]
			quote := attr[len(attr)-1]
			if end := strings.IndexByte(rest, quote); end >= 0 {
				return rest[:end]
			}
		}
	}
	if strings.HasPrefix(opening, "<tool:") {
		name := opening[len("<tool:"):]
		name = strings.TrimRight(name, ">\n\r ")
		return name
	}
	if strings.HasPrefix(opening, "<function=") {
		name := opening[len("<function="):]
		name = strings.TrimRight(name, ">\n\r ")
		return name
	}
	return ""
}

func extractHermesFunctionName(text string) string {
	idx := strings.Index(text, "<function=")
	if idx < 0 {
		return ""
	}
	rest := text[idx+len("<function="):]
	end := strings.IndexAny(rest, ">\n\r ")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func (f *fallbackToolTextFilter) process(final bool) {
	for {
		if f.closingTag != "" {
			if f.activeToolName == "" {
				if name := extractHermesFunctionName(f.pending); name != "" {
					f.activeToolName = name
					if f.emitToolName != nil {
						f.emitToolName(name, f.toolCallIndex)
					}
				}
			}

			closeIndex := closingBoundaryIndex(f.pending, f.closingTag)
			if closeIndex >= 0 {
				body := f.pending[:closeIndex]
				if f.emitToolCall != nil && body != "" {
					f.emitToolCall(body, f.toolCallIndex)
				}
				f.pending = f.pending[closeIndex+len(f.closingTag):]
				// Tolerate a missing '>' after the closing tag (degraded dialect).
				if strings.HasPrefix(f.pending, ">") {
					f.pending = f.pending[1:]
				}
				f.closingTag = ""
				f.activeToolName = ""
				f.toolCallIndex++
				f.discardLeadingWhitespace = true
				continue
			}

			if final {
				if f.emitToolCall != nil && f.pending != "" {
					f.emitToolCall(f.pending, f.toolCallIndex)
				}
				f.pending = ""
				f.closingTag = ""
				f.activeToolName = ""
				f.toolCallIndex++
				return
			}

			keep := longestSuffixMatchingPrefix(f.pending, f.closingTag)
			if keep == 0 {
				if f.emitToolCall != nil && f.pending != "" {
					f.emitToolCall(f.pending, f.toolCallIndex)
				}
				f.pending = ""
			} else {
				emitPart := f.pending[:len(f.pending)-keep]
				if f.emitToolCall != nil && emitPart != "" {
					f.emitToolCall(emitPart, f.toolCallIndex)
				}
				f.pending = f.pending[len(f.pending)-keep:]
			}
			return
		}

		if f.discardLeadingWhitespace {
			trimmed := 0
			for trimmed < len(f.pending) && isFallbackWhitespace(f.pending[trimmed]) {
				trimmed++
			}
			f.pending = f.pending[trimmed:]
			if f.pending == "" {
				return
			}
			f.discardLeadingWhitespace = false
		}

		openIndex := earliestFallbackOpening(f.pending)
		if openIndex < 0 {
			if final {
				f.emitText(f.pending)
				f.pending = ""
				return
			}

			keep := longestFallbackOpeningSuffix(f.pending)
			f.emitText(f.pending[:len(f.pending)-keep])
			if keep == 0 {
				f.pending = ""
			} else {
				f.pending = f.pending[len(f.pending)-keep:]
			}
			return
		}

		if openIndex > 0 {
			f.emitText(f.pending[:openIndex])
			f.pending = f.pending[openIndex:]
		}

		// The opening tag may terminate with '>' or a newline when the model
		// emits the degraded dialect ("<tool_call\n<function=bash\n...").
		openEnd := openingEndIndex(f.pending)
		if openEnd < 0 {
			if final {
				f.pending = ""
				return
			}
			if len(f.pending) > maxFallbackOpeningBytes {
				f.emitText(f.pending[:1])
				f.pending = f.pending[1:]
				continue
			}
			return
		}

		opening := f.pending[:openEnd+1]
		closingTag, valid := fallbackClosingTag(opening)
		if !valid {
			f.emitText(f.pending[:1])
			f.pending = f.pending[1:]
			continue
		}

		f.pending = f.pending[openEnd+1:]
		f.closingTag = closingTag
		toolName := extractFallbackToolName(opening)
		if toolName != "" {
			f.activeToolName = toolName
			if f.emitToolName != nil {
				f.emitToolName(toolName, f.toolCallIndex)
			}
		}
	}
}

func (f *fallbackToolTextFilter) emitText(text string) {
	if text != "" && f.emit != nil {
		f.emit(text)
	}
}

func fallbackClosingTag(opening string) (string, bool) {
	switch {
	case strings.HasPrefix(opening, "<tool_call"):
		if !validFallbackOpeningBoundary(opening, len("<tool_call")) {
			return "", false
		}
		return "</tool_call>", true
	case strings.HasPrefix(opening, "<execute"):
		if !validFallbackOpeningBoundary(opening, len("<execute")) {
			return "", false
		}
		return "</execute>", true
	case strings.HasPrefix(opening, "<tool:"):
		name := opening[len("<tool:") : len(opening)-1]
		if name == "" {
			return "", false
		}
		for _, current := range name {
			if (current < 'a' || current > 'z') &&
				(current < 'A' || current > 'Z') &&
				(current < '0' || current > '9') &&
				current != '_' && current != '-' {
				return "", false
			}
		}
		return "</tool:" + name + ">", true
	case strings.HasPrefix(opening, "<function"):
		if !validFallbackOpeningBoundary(opening, len("<function")) {
			return "", false
		}
		return "</function>", true
	default:
		return "", false
	}
}

// openingEndIndex returns the index of the first '>' or newline that closes
// an opening tag, or -1 if neither is present.
func openingEndIndex(text string) int {
	gt := strings.IndexByte(text, '>')
	nl := strings.IndexByte(text, '\n')
	switch {
	case gt < 0:
		return nl
	case nl < 0:
		return gt
	case nl < gt:
		return nl
	default:
		return gt
	}
}

// closingBoundaryIndex finds a closing tag prefix followed by '>' or whitespace
// (or end of text), so ")tool_call" without '>' still closes the block.
func closingBoundaryIndex(text, prefix string) int {
	for i := 0; i+len(prefix) <= len(text); {
		if !strings.HasPrefix(text[i:], prefix) {
			next := strings.Index(text[i:], prefix)
			if next < 0 {
				return -1
			}
			// Land on the occurrence itself so its boundary byte is inspected.
			i += next
			continue
		}

		end := i + len(prefix)
		if end >= len(text) || text[end] == '>' || isFallbackWhitespace(text[end]) {
			return i
		}
		i++
	}
	return -1
}

func validFallbackOpeningBoundary(opening string, markerLength int) bool {
	if len(opening) <= markerLength {
		return false
	}
	next := opening[markerLength]
	return next == '>' || next == '=' || isFallbackWhitespace(next)
}

func earliestFallbackOpening(text string) int {
	earliest := -1
	for _, marker := range fallbackOpeningMarkers {
		index := strings.Index(text, marker)
		if index >= 0 && (earliest < 0 || index < earliest) {
			earliest = index
		}
	}
	return earliest
}

func longestFallbackOpeningSuffix(text string) int {
	longest := 0
	for _, marker := range fallbackOpeningMarkers {
		if matched := longestSuffixMatchingPrefix(text, marker); matched > longest {
			longest = matched
		}
	}
	return longest
}

func longestSuffixMatchingPrefix(text, marker string) int {
	maxLength := len(text)
	if len(marker)-1 < maxLength {
		maxLength = len(marker) - 1
	}
	for length := maxLength; length > 0; length-- {
		if strings.HasSuffix(text, marker[:length]) {
			return length
		}
	}
	return 0
}

func isFallbackWhitespace(current byte) bool {
	return current == ' ' || current == '\t' || current == '\r' || current == '\n'
}

// StripFallbackToolMarkup removes fallback tool protocol syntax from content
// loaded from older sessions.
func StripFallbackToolMarkup(content string) string {
	var filtered strings.Builder
	filter := newFallbackToolTextFilter(func(text string) {
		filtered.WriteString(text)
	})
	filter.Write(content)
	filter.Flush()
	return filtered.String()
}
