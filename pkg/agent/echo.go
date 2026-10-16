package agent

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

var echoWrappers = []string{`"`, "'", "`", "> ", "# ", "## ", "### ", "#### ", "##### ", "###### ", "**"}

func normalizeDashVariants(s string) string {
	s = strings.ReplaceAll(s, `\u2014`, "-")
	s = strings.ReplaceAll(s, "\u2014", "-")
	s = strings.ReplaceAll(s, "\u2013", "-")
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
	normText.Grow(len(normTarget) + 8)
	lastWasDash := false

	for i := 0; i < len(text); {
		var r rune
		if strings.HasPrefix(text[i:], `\u2014`) {
			r = '-'
			i += 6
		} else {
			var size int
			r, size = utf8.DecodeRuneInString(text[i:])
			if r == '\u2014' || r == '\u2013' {
				r = '-'
			} else {
				r = unicode.ToLower(r)
			}
			i += size
		}

		if r == '-' {
			if lastWasDash {
				continue
			}
			lastWasDash = true
		} else {
			lastWasDash = false
		}

		normText.WriteRune(r)
		currentNorm := normText.String()
		if len(currentNorm) > len(normTarget) || !strings.HasPrefix(normTarget, currentNorm) {
			return -1
		}
		if len(currentNorm) == len(normTarget) {
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

func trimLeadingEchoPunctuation(s string) string {
	s = strings.TrimLeft(s, "\r\n\t ")
	for len(s) > 0 && strings.ContainsRune(".:-,#", rune(s[0])) {
		s = s[1:]
		s = strings.TrimLeft(s, "\r\n\t ")
	}
	return s
}

func trimEchoResidue(rest, wrapper string) string {
	rest = strings.TrimLeft(rest, "\r\n\t ")
	if wrapper != "" && strings.HasPrefix(rest, wrapper) {
		rest = strings.TrimPrefix(rest, wrapper)
	}
	return trimLeadingEchoPunctuation(rest)
}

func StripEchoedPrompt(content, prompt string) string {
	return stripPromptEcho(content, strings.TrimSpace(prompt))
}

type PromptEchoFilter struct {
	normPrompt string
	targets    []string
	held       strings.Builder
	phase      int
}

func NewPromptEchoFilter(prompt string) *PromptEchoFilter {
	norm := strings.TrimSpace(prompt)
	targets := extractPromptTargets(norm)
	return &PromptEchoFilter{normPrompt: norm, targets: targets}
}

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

		f.phase = 2
		f.held.Reset()
		return clean
	}

	return f.skipResidue(f.held.String())
}

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
	s = trimLeadingEchoPunctuation(s)
	if s == "" {
		f.held.Reset()
		return ""
	}
	f.phase = 2
	f.held.Reset()
	return s
}

func (f *PromptEchoFilter) Flush() string {
	if f.phase == 1 {
		f.held.Reset()
		return ""
	}
	out := f.held.String()
	f.held.Reset()
	return out
}