package file

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"loop/pkg/domain/diagnostic"
	"loop/pkg/domain/limits"
)

// ApplyEdits plans a complete edit in memory. On failure it returns the original
// contents, so previews and file writes cannot accept a partial batch.
func ApplyEdits(ctx context.Context, content string, edits []ReplaceEdit, maxBytes int) (string, error) {
	if err := ctx.Err(); err != nil {
		return content, err
	}
	if len(edits) == 0 || len(edits) > maxEdits {
		return content, fmt.Errorf("provide between 1 and %d edits", maxEdits)
	}
	if maxBytes <= 0 || maxBytes > limits.AbsoluteMaxReadFileBytes {
		maxBytes = limits.AbsoluteMaxReadFileBytes
	}
	if len(content) > maxBytes {
		return content, fmt.Errorf("file exceeds edit size limit of %d bytes", maxBytes)
	}
	normalized := make([]ReplaceEdit, len(edits))
	for i, edit := range edits {
		old := strings.ReplaceAll(edit.EffectiveOldText(), "\r\n", "\n")
		replacement := strings.ReplaceAll(edit.EffectiveNewText(), "\r\n", "\n")
		if old == "" {
			return content, fmt.Errorf("edit[%d]: oldText must not be empty", i)
		}
		if len(replacement) > maxBytes {
			return content, fmt.Errorf("edit[%d]: replacement exceeds %d bytes", i, maxBytes)
		}
		if placeholders := DetectOmissionPlaceholders(replacement); len(placeholders) > 0 {
			return content, fmt.Errorf("edit[%d]: provide complete replacement text without omission placeholders", i)
		}
		normalized[i] = ReplaceEdit{OldText: old, NewText: replacement}
	}
	// Independent replacements refer to the original file. Build their result
	// once, in file order, so inserted text cannot become another edit's target.
	if len(normalized) > 1 {
		matches := make([]*textMatch, 0, len(normalized))
		for _, edit := range normalized {
			if err := ctx.Err(); err != nil {
				return content, err
			}
			match, err := locateEditMatch(content, edit.OldText, edit.NewText)
			if err != nil || match == nil {
				break
			}
			matches = append(matches, match)
		}
		if len(matches) == len(normalized) {
			sort.Slice(matches, func(i, j int) bool { return matches[i].startByte < matches[j].startByte })
			overlap := false
			for i := 1; i < len(matches); i++ {
				if matches[i].startByte < matches[i-1].endByte {
					overlap = true
					break
				}
			}
			if !overlap {
				return applyMatches(content, matches, maxBytes)
			}
		}
	}
	// Dependent replacements may refer to the result of an earlier edit.
	updated := content
	for i, edit := range normalized {
		if err := ctx.Err(); err != nil {
			return content, err
		}
		match, err := locateEditMatch(updated, edit.OldText, edit.NewText)
		if err != nil {
			return content, fmt.Errorf("edit[%d]: %w", i, err)
		}
		if match == nil {
			if replacementAlreadyApplied(updated, edit.NewText) {
				continue
			}
			return content, &diagnostic.Error{Code: diagnostic.EditNotFound, Cause: fmt.Errorf("edit[%d]: oldText was not found; read the current file and copy an exact, unique block", i)}
		}
		updated, err = applyMatches(updated, []*textMatch{match}, maxBytes)
		if err != nil {
			return content, fmt.Errorf("edit[%d]: %w", i, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return content, err
	}
	return updated, nil
}

func applyMatches(content string, matches []*textMatch, maxBytes int) (string, error) {
	size := int64(len(content))
	for _, match := range matches {
		size += int64(len(match.newText)) - int64(match.endByte-match.startByte)
	}
	if size > int64(maxBytes) {
		return content, fmt.Errorf("edited file exceeds size limit of %d bytes", maxBytes)
	}
	var result strings.Builder
	result.Grow(int(size))
	position := 0
	for _, match := range matches {
		result.WriteString(content[position:match.startByte])
		result.WriteString(match.newText)
		position = match.endByte
	}
	result.WriteString(content[position:])
	return result.String(), nil
}

func ApplyEditsPreview(content string, edits []ReplaceEdit) (string, bool) {
	updated, err := ApplyEdits(context.Background(), content, edits, limits.AbsoluteMaxReadFileBytes)
	if err != nil {
		return content, false
	}
	return updated, updated != content
}
