package fallback

import (
	domaintool "loop/pkg/domain/tool"
	"sort"
	"strings"
)

const maxFallbackOpeningBytes = 4096

var (
	directToolNames = func() []string {
		names := domaintool.BuiltinToolNames()
		for legacy := range domaintool.LegacyToolNames() {
			names = append(names, legacy)
		}
		sort.Strings(names)
		return names
	}()
	fallbackOpeningMarkers = func() []string {
		markers := []string{"<tool_call", "<execute", "<tool:", "<function"}
		for _, name := range directToolNames {
			markers = append(markers, "<"+name)
		}
		return markers
	}()

	fallbackTargetAttrKeys = [...]string{
		"path", "file", "file_path", "filepath", "filename", "file_name",
		"target", "target_file", "targetfile", "name", "to",
	}

	fallbackBashAttrKeys = [...]string{
		"command", "cmd", "commandline", "script",
	}
)

// ToolTextFilter removes fallback XML tool invocations before assistant text
// reaches the terminal or conversation content.
type ToolTextFilter struct {
	pending                  string
	closingTag               string
	discardLeadingWhitespace bool
	consumeClosingFence      bool
	emit                     func(string)
	flushed                  bool
	plain                    bool
	emitToolName             func(string, int)
	emitToolTarget           func(string, int)
	emitToolCall             func(string, int)
	toolCallIndex            int
	activeToolName           string
}

// NewToolTextFilter creates a new streaming XML tool filter.
func NewToolTextFilter(emit func(string)) *ToolTextFilter {
	return &ToolTextFilter{emit: emit}
}

// NewPlainTextFilter preserves literal text and never interprets tool-like tags.
func NewPlainTextFilter(emit func(string)) *ToolTextFilter {
	return &ToolTextFilter{emit: emit, plain: true}
}

func (f *ToolTextFilter) SetToolCallbacks(emitToolName func(string, int), emitToolCall func(string, int)) {
	if f == nil {
		return
	}
	f.emitToolName = emitToolName
	f.emitToolCall = emitToolCall
}

func (f *ToolTextFilter) SetToolTargetCallback(emitToolTarget func(string, int)) {
	if f == nil {
		return
	}
	f.emitToolTarget = emitToolTarget
}

func (f *ToolTextFilter) Write(chunk string) {
	if f == nil || f.flushed || chunk == "" {
		return
	}
	if f.plain {
		f.emit(chunk)
		return
	}
	f.pending += chunk
	f.process(false)
}

func (f *ToolTextFilter) Flush() {
	if f == nil || f.flushed {
		return
	}
	f.flushed = true
	if f.plain {
		return
	}
	f.process(true)
}

func extractFallbackToolName(opening string) string {
	for _, toolName := range directToolNames {
		if strings.HasPrefix(opening, "<"+toolName) {
			if len(opening) == len(toolName)+1 {
				return toolName
			}
			next := opening[len(toolName)+1]
			if next == ' ' || next == '>' || next == '/' || isFallbackWhitespace(next) {
				return toolName
			}
		}
	}
	for _, attr := range []string{`name="`, `name='`} {
		if _, after, ok := strings.Cut(opening, attr); ok {
			rest := after
			quote := attr[len(attr)-1]
			if before, _, ok := strings.Cut(rest, string(quote)); ok {
				return before
			}
		}
	}
	for _, prefix := range []string{"<tool:", "<function="} {
		if strings.HasPrefix(opening, prefix) {
			name := opening[len(prefix):]
			if end := strings.IndexAny(name, " >\n\r\t"); end >= 0 {
				return name[:end]
			}
			return strings.TrimRight(name, ">\n\r ")
		}
	}
	return ""
}

func extractHermesFunctionName(text string) string {
	_, after, ok := strings.Cut(text, "<function=")
	if !ok {
		return ""
	}
	rest := after
	end := strings.IndexAny(rest, ">\n\r ")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func extractOpeningTarget(toolName, opening string) string {
	attrs := ParseXMLAttrs(opening)
	for _, key := range fallbackTargetAttrKeys {
		if v := strings.TrimSpace(attrs[key]); v != "" {
			return v
		}
	}
	if toolName == "bash" {
		for _, key := range fallbackBashAttrKeys {
			if v := strings.TrimSpace(attrs[key]); v != "" {
				return v
			}
		}
	}
	return ""
}

func (f *ToolTextFilter) process(final bool) {
	for {
		if f.closingTag != "" {
			if f.activeToolName == "" {
				if name := extractHermesFunctionName(f.pending); name != "" {
					f.activeToolName = name
					if f.emitToolName != nil {
						f.emitToolName(domaintool.NormalizeName(name), f.toolCallIndex)
					}
				}
			}

			closeIndex := closingBoundaryIndex(f.pending, f.closingTag)
			if closeIndex >= 0 {
				body := f.pending[:closeIndex]
				if f.emitToolCall != nil && body != "" {
					f.emitToolCall(body, f.toolCallIndex)
				}
				f.pending = strings.TrimPrefix(f.pending[closeIndex+len(f.closingTag):], ">")
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

		if f.consumeClosingFence {
			consumed, wait := tryConsumeClosingFence(f.pending, final)
			if wait {
				return
			}
			if consumed > 0 {
				f.pending = f.pending[consumed:]
				f.consumeClosingFence = false
				f.discardLeadingWhitespace = true
				continue
			}
			if !strings.HasPrefix(f.pending, "<") {
				f.consumeClosingFence = false
			}
		}

		openIndex := earliestFallbackOpening(f.pending)
		if openIndex < 0 {
			if final {
				f.emitText(f.pending)
				f.pending = ""
				return
			}

			keep := longestFallbackOpeningSuffix(f.pending)
			if fenceStart := trailingFenceHoldStart(f.pending); fenceStart >= 0 {
				if fenceKeep := len(f.pending) - fenceStart; fenceKeep > keep {
					keep = fenceKeep
				}
			}
			f.emitText(f.pending[:len(f.pending)-keep])
			if keep == 0 {
				f.pending = ""
			} else {
				f.pending = f.pending[len(f.pending)-keep:]
			}
			return
		}

		hadFence := false
		if openIndex > 0 {
			if fenceStart := trailingOpeningFenceStart(f.pending[:openIndex]); fenceStart >= 0 {
				if fenceStart > 0 {
					f.emitText(f.pending[:fenceStart])
					f.pending = f.pending[fenceStart:]
					openIndex -= fenceStart
				}
				hadFence = true
			} else {
				f.emitText(f.pending[:openIndex])
				f.pending = f.pending[openIndex:]
				openIndex = 0
			}
		}

		openEnd := openingEndIndex(f.pending[openIndex:])
		if openEnd < 0 {
			if final {
				f.pending = ""
				return
			}
			if len(f.pending)-openIndex > maxFallbackOpeningBytes {
				f.emitText(f.pending[:openIndex+1])
				f.pending = f.pending[openIndex+1:]
				continue
			}
			return
		}

		opening := f.pending[openIndex : openIndex+openEnd+1]
		closingTag, valid := fallbackClosingTag(opening)
		if !valid {
			f.emitText(f.pending[:openIndex+1])
			f.pending = f.pending[openIndex+1:]
			continue
		}

		f.pending = f.pending[openIndex+openEnd+1:]
		if hadFence {
			f.consumeClosingFence = true
		}
		f.closingTag = closingTag
		if toolName := extractFallbackToolName(opening); toolName != "" {
			f.activeToolName = toolName
			if f.emitToolName != nil {
				f.emitToolName(domaintool.NormalizeName(toolName), f.toolCallIndex)
			}
			if target := extractOpeningTarget(toolName, opening); target != "" && f.emitToolTarget != nil {
				f.emitToolTarget(target, f.toolCallIndex)
			}
		}

		if closingTag == "" {
			f.activeToolName = ""
			f.toolCallIndex++
			f.discardLeadingWhitespace = true
		}
	}
}

func (f *ToolTextFilter) emitText(text string) {
	if text != "" && f.emit != nil {
		f.emit(text)
	}
}

func fallbackClosingTag(opening string) (string, bool) {
	for _, toolName := range directToolNames {
		if strings.HasPrefix(opening, "<"+toolName) {
			if !validFallbackOpeningBoundary(opening, len("<"+toolName)) {
				continue
			}
			if strings.HasSuffix(strings.TrimSpace(opening), "/>") {
				return "", true
			}
			return "</" + toolName + ">", true
		}
	}
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
		if idx := strings.IndexAny(name, " \t\r\n"); idx >= 0 {
			name = name[:idx]
		}
		if !isValidXMLTagIdent(name) {
			return "", false
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

func closingBoundaryIndex(text, prefix string) int {
	for i := 0; i+len(prefix) <= len(text); {
		if !strings.HasPrefix(text[i:], prefix) {
			next := strings.Index(text[i:], prefix)
			if next < 0 {
				return -1
			}
			i += next
			continue
		}

		end := i + len(prefix)
		if strings.HasSuffix(prefix, ">") || end >= len(text) || text[end] == '>' || isFallbackWhitespace(text[end]) {
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
	return next == '>' || next == '/' || next == '=' || isFallbackWhitespace(next)
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
	maxLength := min(len(marker)-1, len(text))
	for length := maxLength; length > 0; length-- {
		if strings.HasSuffix(text, marker[:length]) {
			return length
		}
	}
	return 0
}

func isValidXMLTagIdent(name string) bool {
	if name == "" {
		return false
	}
	for _, current := range name {
		if (current < 'a' || current > 'z') &&
			(current < 'A' || current > 'Z') &&
			(current < '0' || current > '9') &&
			current != '_' && current != '-' {
			return false
		}
	}
	return true
}

func isFallbackWhitespace(current byte) bool {
	return current == ' ' || current == '\t' || current == '\r' || current == '\n'
}

func isFenceLangByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-'
}

// isOpeningFenceLine reports whether line is a markdown code fence opener
// such as "```" or "```xml".
func isOpeningFenceLine(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	if len(line)-len(trimmed) > 3 {
		return false
	}
	ticks := 0
	for ticks < len(trimmed) && trimmed[ticks] == '`' {
		ticks++
	}
	if ticks < 3 {
		return false
	}
	rest := strings.TrimRight(trimmed[ticks:], " \t\r")
	if len(rest) > 24 {
		return false
	}
	for i := 0; i < len(rest); i++ {
		if !isFenceLangByte(rest[i]) {
			return false
		}
	}
	return true
}

// trailingOpeningFenceStart returns the start index of a markdown code fence
// opener line at the end of prefix (followed only by whitespace including a
// newline), or -1 if prefix does not end with one.
func trailingOpeningFenceStart(prefix string) int {
	end := len(prefix)
	hasNewline := false
	for end > 0 && isFallbackWhitespace(prefix[end-1]) {
		if prefix[end-1] == '\n' {
			hasNewline = true
		}
		end--
	}
	if !hasNewline || end == 0 {
		return -1
	}
	lineStart := strings.LastIndexByte(prefix[:end], '\n') + 1
	if !isOpeningFenceLine(prefix[lineStart:end]) {
		return -1
	}
	return lineStart
}

// trailingFenceHoldStart returns the start index of a potential opening fence
// at the tail of text that might precede a fallback tool opening tag in a later
// stream chunk, or -1 if no fence needs to be buffered.
func trailingFenceHoldStart(text string) int {
	// Case 1: incomplete fence opener on the final line (e.g. "`", "``", "```xml").
	lastNL := strings.LastIndexByte(text, '\n')
	tail := text[lastNL+1:]
	if tail != "" && len(tail) <= 32 {
		trimmed := strings.TrimLeft(tail, " \t")
		if len(tail)-len(trimmed) <= 3 && strings.HasPrefix(trimmed, "`") {
			ticks := 0
			for ticks < len(trimmed) && trimmed[ticks] == '`' {
				ticks++
			}
			if ticks < 3 && ticks == len(trimmed) {
				return lastNL + 1
			}
			if ticks >= 3 && isOpeningFenceLine(tail) {
				return lastNL + 1
			}
		}
	}

	// Case 2: complete opening fence line followed only by whitespace and an
	// optional prefix of a fallback opening marker (e.g. "```xml\n<tool_").
	trimTail := strings.TrimLeft(tail, " \t\r")
	if trimTail != "" && !isPrefixOfAnyFallbackMarker(trimTail) {
		return -1
	}
	beforeTail := text[:lastNL+1]
	if fenceStart := trailingOpeningFenceStart(beforeTail); fenceStart >= 0 && len(text)-fenceStart <= 64 {
		return fenceStart
	}
	return -1
}

func isPrefixOfAnyFallbackMarker(s string) bool {
	for _, marker := range fallbackOpeningMarkers {
		if strings.HasPrefix(marker, s) {
			return true
		}
	}
	return false
}

// tryConsumeClosingFence checks whether text starts with a closing markdown
// code fence line ("```" followed by optional horizontal whitespace and a
// newline or EOF).
func tryConsumeClosingFence(text string, final bool) (consumed int, wait bool) {
	if text == "" {
		return 0, false
	}
	if text[0] != '`' {
		return 0, false
	}
	i := 0
	for i < len(text) && text[i] == '`' {
		i++
	}
	if i < 3 {
		if !final && i == len(text) {
			return 0, true
		}
		return 0, false
	}
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	if i == len(text) {
		if !final {
			return 0, true
		}
		return i, false
	}
	if text[i] == '\r' {
		i++
		if i == len(text) {
			if !final {
				return 0, true
			}
			return i, false
		}
	}
	if text[i] == '\n' {
		return i + 1, false
	}
	return 0, false
}

// StripFallbackToolMarkup removes fallback tool protocol syntax from content.
func StripFallbackToolMarkup(content string) string {
	var filtered strings.Builder
	filter := NewToolTextFilter(func(text string) {
		filtered.WriteString(text)
	})
	filter.Write(content)
	filter.Flush()
	return filtered.String()
}
