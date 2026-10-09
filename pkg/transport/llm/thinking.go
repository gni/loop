package llm

import (
	"strings"
)

// ThinkingStreamFilter parses and separates reasoning tags from standard content streams.
// It supports both <think>...</think> and <|channel>thought...<channel|> delimiters across chunk boundaries.
type ThinkingStreamFilter struct {
	buffer        string
	inThoughtMode bool
	onContent     func(string)
	onReasoning   func(string)
}

// NewThinkingStreamFilter creates a new thinking filter with content and reasoning token callbacks.
func NewThinkingStreamFilter(onContent func(string), onReasoning func(string)) *ThinkingStreamFilter {
	return &ThinkingStreamFilter{
		onContent:   onContent,
		onReasoning: onReasoning,
	}
}

// Feed receives a raw incoming chunk and routes content/reasoning tokens to their respective handlers.
func findEarliestTag(buf string, tags []string) (int, string) {
	earliestIdx := -1
	earliestTag := ""
	for _, t := range tags {
		idx := strings.Index(buf, t)
		if idx != -1 && (earliestIdx == -1 || idx < earliestIdx) {
			earliestIdx = idx
			earliestTag = t
		}
	}
	return earliestIdx, earliestTag
}

func matchPartialTagSuffix(buf string, tags []string) int {
	var maxMatched int
	for _, t := range tags {
		for i := len(t) - 1; i >= 1; i-- {
			if strings.HasSuffix(buf, t[:i]) {
				if i > maxMatched {
					maxMatched = i
				}
				break
			}
		}
	}
	return maxMatched
}

func (f *ThinkingStreamFilter) Feed(chunk string) {
	if chunk == "" {
		return
	}
	f.buffer += chunk

	for {
		var tags []string
		var emit func(string)
		var nextMode bool

		if !f.inThoughtMode {
			tags = []string{"<|channel>thought", "<think>"}
			emit = f.onContent
			nextMode = true
		} else {
			tags = []string{"<channel|>", "</think>"}
			emit = f.onReasoning
			nextMode = false
		}

		if idx, tag := findEarliestTag(f.buffer, tags); idx != -1 {
			pre := f.buffer[:idx]
			if pre != "" && emit != nil {
				emit(pre)
			}
			f.buffer = f.buffer[idx+len(tag):]
			f.inThoughtMode = nextMode
			continue
		}

		prefixMatched := matchPartialTagSuffix(f.buffer, tags)
		if prefixMatched > 0 {
			sendLen := len(f.buffer) - prefixMatched
			if sendLen > 0 {
				pre := f.buffer[:sendLen]
				if emit != nil {
					emit(pre)
				}
				f.buffer = f.buffer[sendLen:]
			}
			break
		}

		if emit != nil {
			emit(f.buffer)
		}
		f.buffer = ""
		break
	}
}

// Flush emits any remaining buffered text when stream ends.
func (f *ThinkingStreamFilter) Flush() {
	if f.buffer == "" {
		return
	}
	if f.inThoughtMode {
		if f.onReasoning != nil {
			f.onReasoning(f.buffer)
		}
	} else {
		if f.onContent != nil {
			f.onContent(f.buffer)
		}
	}
	f.buffer = ""
}
