package agent

import (
	"sort"
	"strings"
	"unicode"

	"loop/pkg/agent/run"
	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/domain/task"
)

type relevantText struct {
	text         string
	index, score int
}

func (a *Agent) knowledgeObjective() string {
	if current := run.TaskSnapshot(a.Context()); task.HasWork(current) {
		return current.Objective
	}
	return run.Objective(a.Context())
}

// Select optional knowledge only. Explicit instructions and loaded skill bodies
// never pass through this allocation or have their contents rewritten.
func relevantKnowledge(text, query string, budget int) string {
	if len(text) <= budget {
		return text
	}
	terms := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' })
	var blocks []relevantText
	for index, block := range strings.Split(text, "\n\n") {
		score := 0
		lower := strings.ToLower(block)
		for _, term := range terms {
			if len(term) > 2 && strings.Contains(lower, term) {
				score++
			}
		}
		blocks = append(blocks, relevantText{text: block, index: index, score: score})
	}
	sort.SliceStable(blocks, func(i, j int) bool { return blocks[i].score > blocks[j].score })
	var selected []relevantText
	remaining := max(0, budget-96)
	for _, block := range blocks {
		if remaining <= 0 {
			break
		}
		block.text = filetool.UTF8SafePrefix(block.text, min(remaining, 2048))
		selected = append(selected, block)
		remaining -= len(block.text) + 2
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].index < selected[j].index })
	var output strings.Builder
	for _, block := range selected {
		if output.Len() > 0 {
			output.WriteString("\n\n")
		}
		output.WriteString(block.text)
	}
	output.WriteString("\n[Optional memory excerpt; read the named file for full context.]")
	return output.String()
}
