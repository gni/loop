package skills

import (
	"sort"
	"strings"

	"loop/pkg/agent/tool"
	filetool "loop/pkg/agent/tool/file"
)

func Relevant(skills []tool.Skill, query string, budget int) []tool.Skill {
	if budget <= 0 {
		return skills
	}
	type match struct {
		skill tool.Skill
		score int
	}
	var matches []match
	for _, skill := range skills {
		if skill.DisableModelInvocation {
			continue
		}
		text := strings.ToLower(skill.Name + " " + skill.Description)
		score := 0
		for term := range strings.FieldsSeq(strings.ToLower(query)) {
			if len(term) > 2 && strings.Contains(text, term) {
				score++
			}
		}
		skill.Description = filetool.UTF8SafePrefix(skill.Description, 160)
		matches = append(matches, match{skill, score})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].skill.Name < matches[j].skill.Name
	})
	var selected []tool.Skill
	used := 0
	for _, entry := range matches {
		cost := len(entry.skill.Name) + len(entry.skill.Description) + 16
		if used+cost > budget {
			continue
		}
		selected = append(selected, entry.skill)
		used += cost
	}
	return selected
}
