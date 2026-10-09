package agent

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// echoWrappers are the ways a model wraps an echoed prompt before thinking or writing.
var echoWrappers = []string{`"`, "'", "`", "> ", "# ", "## ", "### ", "#### ", "##### ", "###### ", "**"}

func normalizeDashVariants(s string) string {
	s = strings.ReplaceAll(s, `\u2014`, "-")
	s = strings.ReplaceAll(s, "\u2014", "-") // em-dash —
	s = strings.ReplaceAll(s, "\u2013", "-") // en-dash –
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return s
}

func matchNormalizedPrefix(text, target string) int {
	normTarget := normalizeDashVariants(strings.ToLower(strings.TrimSpace(target)))
	if normTarget == "" {
		return -1
	}

	var normText strings.Builder
	for i := 0; i < len(text); {
		if strings.HasPrefix(text[i:], `\u2014`) {
			normText.WriteString("-")
			i += 6
		} else {
			r, size := utf8.DecodeRuneInString(text[i:])
			if r == '\u2014' || r == '\u2013' {
				normText.WriteString("-")
			} else {
				normText.WriteRune(unicode.ToLower(r))
			}
			i += size
		}
		currentNorm := normText.String()
		for strings.Contains(currentNorm, "--") {
			currentNorm = strings.ReplaceAll(currentNorm, "--", "-")
		}
		if currentNorm == normTarget {
			return i
		}
	}
	return -1
}

func extractPromptTargets(prompt string) []string {
	norm := strings.TrimSpace(prompt)
	if norm == "" {
		return nil
	}
	seen := make(map[string]bool)
	var targets []string
	addTarget := func(t string) {
		t = strings.TrimSpace(t)
		if t != "" && !seen[t] {
			seen[t] = true
			targets = append(targets, t)
		}
	}

	addTarget(norm)

	// If prompt has multiple lines, also match the title/first line
	lines := strings.Split(norm, "\n")
	if len(lines) > 1 {
		firstLine := strings.TrimSpace(lines[0])
		if len(firstLine) >= 3 {
			addTarget(firstLine)
			cleanFirst := strings.TrimLeft(firstLine, "#*_ \t\"'`")
			if len(cleanFirst) >= 3 {
				addTarget(cleanFirst)
			}
		}
	}

	// Also add title with leading markdown heading stripped if present
	cleanNorm := strings.TrimLeft(norm, "#*_ \t\"'`")
	if len(cleanNorm) >= 3 {
		addTarget(cleanNorm)
	}

	return targets
}

func matchPromptInText(clean string, targets []string) (matchedTarget string, remainder string, wrapper string) {
	for _, target := range targets {
		for _, w := range echoWrappers {
			if strings.HasPrefix(strings.ToLower(clean), strings.ToLower(w)) {
				body := clean[len(w):]
				if cut := matchNormalizedPrefix(body, target); cut != -1 {
					return target, body[cut:], w
				}
			}
		}

		if cut := matchNormalizedPrefix(clean, target); cut != -1 {
			return target, clean[cut:], ""
		}
	}
	return "", "", ""
}

// stripPromptEcho removes a leading echoed prompt (optionally wrapped) plus the
// closing wrapper and any separator punctuation that follows it.
func stripPromptEcho(reasoning, normPrompt string) string {
	if normPrompt == "" || reasoning == "" {
		return reasoning
	}
	clean := strings.TrimLeft(reasoning, "\r\n\t ")
	targets := extractPromptTargets(normPrompt)

	_, rem, wrapper := matchPromptInText(clean, targets)
	if rem != "" || wrapper != "" {
		return trimEchoResidue(rem, wrapper)
	}
	return reasoning
}

// trimEchoResidue drops the closing wrapper and the separator punctuation the model
// uses to transition from the echo into its actual thought or text.
func trimEchoResidue(rest, wrapper string) string {
	rest = strings.TrimLeft(rest, "\r\n\t ")
	if wrapper != "" && strings.HasPrefix(rest, wrapper) {
		rest = strings.TrimPrefix(rest, wrapper)
	}
	rest = strings.TrimLeft(rest, "\r\n\t ")
	for len(rest) > 0 && strings.ContainsRune(".:-,#", rune(rest[0])) {
		rest = rest[1:]
		rest = strings.TrimLeft(rest, "\r\n\t ")
	}
	return rest
}

// StripEchoedPrompt strips leading echoed prompt text and trailing newlines/whitespace
// from model content if the model begins by repeating the user's prompt or title.
func StripEchoedPrompt(content, prompt string) string {
	return stripPromptEcho(content, strings.TrimSpace(prompt))
}

// PromptEchoFilter suppresses an echoed prompt as it streams. Post-hoc stripping was
// not enough: chunks are printed the moment they arrive, so a model that opens its
// response by repeating the prompt printed the echo (and the trailing quote and
// separator punctuation) before anything could be removed. The filter holds back only
// the runes needed to decide, then passes everything through.
type PromptEchoFilter struct {
	normPrompt string
	targets    []string
	held       strings.Builder
	phase      int // 0 deciding, 1 skipping echo residue, 2 passing through
}

func NewPromptEchoFilter(prompt string) *PromptEchoFilter {
	norm := strings.TrimSpace(prompt)
	targets := extractPromptTargets(norm)
	return &PromptEchoFilter{normPrompt: norm, targets: targets}
}

// Write returns the portion of chunk that is safe to print.
func (f *PromptEchoFilter) Write(chunk string) string {
	if f.phase == 2 || f.normPrompt == "" || len(f.targets) == 0 {
		return chunk
	}
	f.held.WriteString(chunk)

	if f.phase == 0 {
		clean := strings.TrimLeft(f.held.String(), "\r\n\t ")

		matchedTarget, rem, _ := matchPromptInText(clean, f.targets)
		if matchedTarget != "" {
			f.held.Reset()
			f.phase = 1
			return f.skipResidue(rem)
		}

		cleanDash := normalizeDashVariants(clean)
		lowerCleanDash := strings.ToLower(cleanDash)

		// Ambiguous: the echo could still complete in a later chunk, so hold.
		for _, target := range f.targets {
			normTarget := normalizeDashVariants(target)
			lowerTarget := strings.ToLower(normTarget)
			if len(lowerCleanDash) < len(lowerTarget) && strings.HasPrefix(lowerTarget, lowerCleanDash) {
				return ""
			}
			for _, wrapper := range echoWrappers {
				wDash := normalizeDashVariants(wrapper)
				if strings.HasPrefix(lowerCleanDash, strings.ToLower(wDash)) {
					bodyDash := lowerCleanDash[len(wDash):]
					if len(bodyDash) < len(lowerTarget) && strings.HasPrefix(lowerTarget, bodyDash) {
						return ""
					}
				} else if len(lowerCleanDash) < len(wDash) && strings.HasPrefix(strings.ToLower(wDash), lowerCleanDash) {
					return ""
				}
			}
		}

		// Confirmed not an echo: emit what was held.
		f.phase = 2
		f.held.Reset()
		return clean
	}

	return f.skipResidue(f.held.String())
}

// skipResidue consumes the closing wrapper and separator punctuation that separates the
// echo from the real text, and emits from the first character of that text.
func (f *PromptEchoFilter) skipResidue(s string) string {
	for _, wrapper := range echoWrappers {
		s = strings.TrimLeft(s, "\r\n\t ")
		if s == "" {
			f.held.Reset()
			return ""
		}
		if strings.HasPrefix(s, wrapper) {
			s = s[len(wrapper):]
			break
		}
	}
	s = strings.TrimLeft(s, "\r\n\t ")
	for len(s) > 0 && strings.ContainsRune(".:-,#", rune(s[0])) {
		s = s[1:]
		s = strings.TrimLeft(s, "\r\n\t ")
	}
	if s == "" {
		f.held.Reset()
		return ""
	}
	f.phase = 2
	f.held.Reset()
	return s
}

// Flush returns anything still held when the stream ends before the decision resolved.
func (f *PromptEchoFilter) Flush() string {
	if f.phase == 1 {
		f.held.Reset()
		return ""
	}
	out := f.held.String()
	f.held.Reset()
	return out
}
