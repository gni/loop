package file

import (
	"fmt"
	"strings"
	"unicode"
)

func normalizeForFuzzyMatch(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch r {
		case '\u2018', '\u2019', '\u201a', '\u201b': // Smart single quotes
			b.WriteRune('\'')
		case '\u201c', '\u201d', '\u201e', '\u201f': // Smart double quotes
			b.WriteRune('"')
		case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212': // Dashes & minus
			b.WriteRune('-')
		case '\u00a0', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200a', '\u202f', '\u205f', '\u3000': // Unicode spaces
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(normalizeForFuzzyMatch(s)), " ")
}

func replacementAlreadyApplied(content, newText string) bool {
	trimmed := strings.TrimSpace(newText)
	if trimmed == "" {
		return false
	}
	if len(trimmed) < 16 && !strings.Contains(newText, "\n") {
		return false
	}
	return strings.Count(content, newText) == 1
}

type textMatch struct {
	startByte   int
	endByte     int
	matchedText string
	newText     string
}

type wordToken struct {
	text      string
	norm      string
	startByte int
	endByte   int
}

func extractWordTokens(s string) []wordToken {
	var tokens []wordToken
	inWord := false
	start := 0
	for i, r := range s {
		if unicode.IsSpace(r) {
			if inWord {
				word := s[start:i]
				tokens = append(tokens, wordToken{
					text:      word,
					norm:      strings.ToLower(normalizeForFuzzyMatch(word)),
					startByte: start,
					endByte:   i,
				})
				inWord = false
			}
		} else {
			if !inWord {
				start = i
				inWord = true
			}
		}
	}
	if inWord {
		word := s[start:]
		tokens = append(tokens, wordToken{
			text:      word,
			norm:      strings.ToLower(normalizeForFuzzyMatch(word)),
			startByte: start,
			endByte:   len(s),
		})
	}
	return tokens
}

func normalizeBullet(s string) string {
	if s == "*" || s == "-" || s == "+" || s == "•" {
		return "-"
	}
	return s
}

func matchWordTokens(cw, ow wordToken) bool {
	if cw.norm == ow.norm {
		return true
	}
	if normalizeBullet(cw.norm) == normalizeBullet(ow.norm) {
		return true
	}
	cTrim := strings.TrimRight(cw.norm, ".,;:!?")
	oTrim := strings.TrimRight(ow.norm, ".,;:!?")
	if cTrim == oTrim && cTrim != "" {
		return true
	}
	return false
}

func leadingWhitespace(s string) string {
	for i, r := range s {
		if r != ' ' && r != '\t' {
			return s[:i]
		}
	}
	return s
}

func locateEditMatch(content, oldText, newText string) (*textMatch, error) {
	if oldText == "" {
		return nil, nil
	}

	// Tier 1: Exact substring match
	exactCount := strings.Count(content, oldText)
	if exactCount == 1 {
		start := strings.Index(content, oldText)
		return &textMatch{
			startByte:   start,
			endByte:     start + len(oldText),
			matchedText: oldText,
			newText:     newText,
		}, nil
	}
	if exactCount > 1 {
		return nil, fmt.Errorf("oldText block is not unique; found %d occurrences in file", exactCount)
	}

	// Tier 2: Unicode and quote/dash/space normalization
	normContent := normalizeForFuzzyMatch(content)
	normOld := normalizeForFuzzyMatch(oldText)
	if normOld != oldText || normContent != content {
		normCount := strings.Count(normContent, normOld)
		if normCount == 1 {
			normIdx := strings.Index(normContent, normOld)
			normRunes := []rune(normContent)
			contentRunes := []rune(content)
			if len(normRunes) == len(contentRunes) {
				runeStart := len([]rune(normContent[:normIdx]))
				runeLen := len([]rune(normOld))
				if runeStart+runeLen <= len(contentRunes) {
					matchedRunes := contentRunes[runeStart : runeStart+runeLen]
					matchedText := string(matchedRunes)
					startByte := len(string(contentRunes[:runeStart]))
					endByte := startByte + len(matchedText)
					if endByte <= len(content) && content[startByte:endByte] == matchedText {
						return &textMatch{
							startByte:   startByte,
							endByte:     endByte,
							matchedText: matchedText,
							newText:     newText,
						}, nil
					}
				}
			}
		} else if normCount > 1 {
			return nil, fmt.Errorf("oldText block is not unique; found %d occurrences in file", normCount)
		}
	}

	// Tier 3: Line-by-line whitespace-trimmed and normalized matching
	fileLines := strings.Split(content, "\n")
	oldLines := strings.Split(oldText, "\n")

	lineStarts := make([]int, len(fileLines))
	lineEnds := make([]int, len(fileLines))
	offset := 0
	for idx, fl := range fileLines {
		lineStarts[idx] = offset
		offset += len(fl)
		lineEnds[idx] = offset
		offset++ // for newline
	}

	cleanOldLines := make([]string, len(oldLines))
	for i, l := range oldLines {
		cleanOldLines[i] = strings.TrimSpace(l)
	}

	startIdx := 0
	for startIdx < len(cleanOldLines) && cleanOldLines[startIdx] == "" {
		startIdx++
	}
	endIdx := len(cleanOldLines)
	for endIdx > startIdx && cleanOldLines[endIdx-1] == "" {
		endIdx--
	}
	coreOldLines := cleanOldLines[startIdx:endIdx]

	if len(coreOldLines) > 0 {
		matchStart := -1
		matchEnd := -1
		matchesCount := 0

		for fs := 0; fs <= len(fileLines)-len(coreOldLines); fs++ {
			matched := true
			for j := 0; j < len(coreOldLines); j++ {
				fileLineNorm := normalizeSpace(fileLines[fs+j])
				oldLineNorm := normalizeSpace(coreOldLines[j])
				if fileLineNorm != oldLineNorm {
					matched = false
					break
				}
			}
			if matched {
				matchStart = fs
				matchEnd = fs + len(coreOldLines)
				matchesCount++
			}
		}

		if matchesCount == 1 {
			actualStart := matchStart
			for actualStart > 0 && matchStart-actualStart < startIdx {
				if strings.TrimSpace(fileLines[actualStart-1]) == "" {
					actualStart--
				} else {
					break
				}
			}
			actualEnd := matchEnd
			for actualEnd < len(fileLines) && actualEnd-matchEnd < (len(cleanOldLines)-endIdx) {
				if strings.TrimSpace(fileLines[actualEnd]) == "" {
					actualEnd++
				} else {
					break
				}
			}
			startByte := lineStarts[actualStart]
			endByte := lineEnds[actualEnd-1]
			matchedText := content[startByte:endByte]

			adjustedNew := newText
			fileIndent := leadingWhitespace(fileLines[actualStart])
			oldIndent := leadingWhitespace(oldLines[0])
			if fileIndent != oldIndent && strings.HasPrefix(fileIndent, oldIndent) {
				indentDiff := fileIndent[len(oldIndent):]
				if indentDiff != "" {
					newLines := strings.Split(newText, "\n")
					allIndented := true
					for _, nl := range newLines {
						if nl != "" && !strings.HasPrefix(nl, fileIndent) {
							allIndented = false
							break
						}
					}
					if !allIndented {
						for k := range newLines {
							if newLines[k] != "" {
								newLines[k] = indentDiff + newLines[k]
							}
						}
						adjustedNew = strings.Join(newLines, "\n")
					}
				}
			}

			return &textMatch{
				startByte:   startByte,
				endByte:     endByte,
				matchedText: matchedText,
				newText:     adjustedNew,
			}, nil
		}
		if matchesCount > 1 {
			return nil, fmt.Errorf("oldText block is not unique; found %d occurrences in file", matchesCount)
		}
	}

	// Tier 4: Word-stream sequence matching (Markdown soft-wraps, paragraphs, bullets)
	contentWords := extractWordTokens(content)
	oldWords := extractWordTokens(oldText)
	if len(oldWords) >= 2 || (len(oldWords) == 1 && len(oldWords[0].text) >= 6) {
		matchStart := -1
		matchCount := 0
		for i := 0; i <= len(contentWords)-len(oldWords); i++ {
			matched := true
			for j := 0; j < len(oldWords); j++ {
				if !matchWordTokens(contentWords[i+j], oldWords[j]) {
					matched = false
					break
				}
			}
			if matched {
				matchStart = i
				matchCount++
			}
		}

		if matchCount == 1 && matchStart >= 0 {
			startByte := contentWords[matchStart].startByte
			endByte := contentWords[matchStart+len(oldWords)-1].endByte

			if strings.HasPrefix(oldText, "\n") || leadingWhitespace(oldText) != "" {
				lineStart := strings.LastIndex(content[:startByte], "\n")
				if lineStart >= 0 {
					prefix := content[lineStart+1 : startByte]
					if strings.TrimSpace(prefix) == "" || normalizeBullet(strings.TrimSpace(prefix)) == "-" {
						startByte = lineStart + 1
					}
				} else if strings.TrimSpace(content[:startByte]) == "" {
					startByte = 0
				}
			}
			if strings.HasSuffix(oldText, "\n") {
				lineEnd := strings.Index(content[endByte:], "\n")
				if lineEnd >= 0 {
					endByte += lineEnd + 1
				}
			}

			matchedText := content[startByte:endByte]
			return &textMatch{
				startByte:   startByte,
				endByte:     endByte,
				matchedText: matchedText,
				newText:     newText,
			}, nil
		}
		if matchCount > 1 {
			return nil, fmt.Errorf("oldText block is not unique; found %d occurrences in file", matchCount)
		}
	}

	// Tier 5: Single-line / Partial-line substring resilient match
	if len(oldLines) == 1 && strings.TrimSpace(oldText) != "" {
		trimmedOld := strings.TrimSpace(oldText)
		oldNorm := normalizeSpace(trimmedOld)
		oldLower := strings.ToLower(oldNorm)

		matchLine := -1
		matchPhraseStart := -1
		matchPhraseEnd := -1
		matchCount := 0

		for idx, fl := range fileLines {
			flNorm := normalizeSpace(fl)
			flLower := strings.ToLower(flNorm)
			if strings.Contains(flLower, oldLower) || strings.Contains(flNorm, oldNorm) {
				flOrigLower := strings.ToLower(fl)
				subIdx := strings.Index(flOrigLower, strings.ToLower(trimmedOld))
				if subIdx >= 0 {
					matchLine = idx
					matchPhraseStart = subIdx
					matchPhraseEnd = subIdx + len(trimmedOld)
					matchCount++
				}
			}
		}

		if matchCount == 1 && matchLine >= 0 {
			startByte := lineStarts[matchLine] + matchPhraseStart
			endByte := lineStarts[matchLine] + matchPhraseEnd
			if endByte <= len(content) {
				return &textMatch{
					startByte:   startByte,
					endByte:     endByte,
					matchedText: content[startByte:endByte],
					newText:     newText,
				}, nil
			}
		}
		if matchCount > 1 {
			return nil, fmt.Errorf("oldText block is not unique; found %d occurrences in file", matchCount)
		}
	}

	// Tier 6: Multi-line sliding window fuzzy similarity match (Levenshtein / token similarity)
	if len(coreOldLines) >= 1 {
		bestMatchStart := -1
		bestMatchScore := 0.0
		bestMatchCount := 0

		for fs := 0; fs <= len(fileLines)-len(coreOldLines); fs++ {
			score := 0.0
			for j := 0; j < len(coreOldLines); j++ {
				fNorm := normalizeSpace(fileLines[fs+j])
				oNorm := normalizeSpace(coreOldLines[j])
				if fNorm == oNorm {
					score += 1.0
				} else if strings.Contains(fNorm, oNorm) || strings.Contains(oNorm, fNorm) {
					score += 0.8
				} else {
					fWords := strings.Fields(strings.ToLower(fNorm))
					oWords := strings.Fields(strings.ToLower(oNorm))
					if len(fWords) > 0 && len(oWords) > 0 {
						common := 0
						for _, ow := range oWords {
							for _, fw := range fWords {
								if ow == fw {
									common++
									break
								}
							}
						}
						wordScore := float64(common) / float64(len(oWords))
						if wordScore >= 0.5 {
							score += wordScore * 0.7
						}
					}
				}
			}
			normalizedScore := score / float64(len(coreOldLines))
			minThreshold := 0.75
			if len(coreOldLines) <= 2 {
				minThreshold = 0.80
			}
			if normalizedScore >= minThreshold {
				if normalizedScore > bestMatchScore {
					bestMatchScore = normalizedScore
					bestMatchStart = fs
					bestMatchCount = 1
				} else if normalizedScore == bestMatchScore {
					bestMatchCount++
				}
			}
		}

		if bestMatchCount == 1 && bestMatchStart >= 0 {
			actualStart := bestMatchStart
			actualEnd := bestMatchStart + len(coreOldLines)
			startByte := lineStarts[actualStart]
			endByte := lineEnds[actualEnd-1]
			matchedText := content[startByte:endByte]
			return &textMatch{
				startByte:   startByte,
				endByte:     endByte,
				matchedText: matchedText,
				newText:     newText,
			}, nil
		}
		if bestMatchCount > 1 {
			return nil, fmt.Errorf("oldText block is not unique; found %d occurrences in file", bestMatchCount)
		}
	}

	return nil, nil
}

func findClosestLineMatch(content, oldText string) int {
	fileLines := strings.Split(content, "\n")
	oldLines := strings.Split(oldText, "\n")
	var coreLine string
	for _, l := range oldLines {
		t := strings.TrimSpace(l)
		if len(t) > 3 {
			coreLine = t
			break
		}
	}
	if coreLine == "" {
		return 0
	}
	coreNorm := normalizeSpace(coreLine)
	coreLower := strings.ToLower(coreNorm)

	// Pass 1: exact or normalized match
	for idx, fLine := range fileLines {
		if strings.Contains(fLine, coreLine) || normalizeSpace(fLine) == coreNorm {
			return idx + 1
		}
	}

	// Pass 2: case-insensitive match
	for idx, fLine := range fileLines {
		fNorm := strings.ToLower(normalizeSpace(fLine))
		if strings.Contains(fNorm, coreLower) {
			return idx + 1
		}
	}

	// Pass 3: highest word overlap
	coreWords := strings.Fields(coreLower)
	if len(coreWords) >= 2 {
		bestIdx := -1
		bestScore := 0
		for idx, fLine := range fileLines {
			fWords := strings.Fields(strings.ToLower(fLine))
			score := 0
			for _, cw := range coreWords {
				for _, fw := range fWords {
					if cw == fw || strings.Contains(fw, cw) {
						score++
						break
					}
				}
			}
			if score > bestScore && score >= len(coreWords)/2 && score >= 2 {
				bestScore = score
				bestIdx = idx
			}
		}
		if bestIdx >= 0 {
			return bestIdx + 1
		}
	}

	return 0
}

func renderLineSnippet(fileLines []string, centerLine int, radius int) string {
	start := centerLine - radius - 1
	if start < 0 {
		start = 0
	}
	end := centerLine + radius
	if end > len(fileLines) {
		end = len(fileLines)
	}
	var sb strings.Builder
	for i := start; i < end; i++ {
		marker := " "
		if i == centerLine-1 {
			marker = ">"
		}
		sb.WriteString(fmt.Sprintf("%s %4d | %s\n", marker, i+1, fileLines[i]))
	}
	return strings.TrimRight(sb.String(), "\n")
}
