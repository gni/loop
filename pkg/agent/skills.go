package agent

import (
	"io"

	"loop/pkg/agent/skills"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
)

type Skill = skills.Skill

func resolveSkillDir(dir string) string {
	return skills.ResolveDir(dir)
}

func LoadSkillsFromDirs(dirs ...string) ([]Skill, error) {
	return skills.LoadFromDirs(dirs...)
}

func loadSkillsFromDirs(warnings io.Writer, dirs ...string) ([]Skill, error) {
	return skills.LoadFromDirsWithWarnings(warnings, dirs...)
}

func SkillSearchDirs(skillsDir, workspaceRoot string) []string {
	return skills.SearchDirs(skillsDir, workspaceRoot)
}

func SkillSearchDirsForWorkspace(skillsDir, workspaceRoot string, trusted bool) []string {
	return skills.SearchDirsForWorkspace(skillsDir, workspaceRoot, trusted)
}

func relevantSkills(list []tool.Skill, query string, budget int) []tool.Skill {
	return skills.Relevant(list, query, budget)
}

func isSkillInstruction(msg db.Message) bool {
	return skills.IsInstruction(msg)
}

func loadedSkillMessages(messages []db.Message) []db.Message {
	return skills.LoadedMessages(messages)
}

func skillContextMessages(messages []db.Message) []db.Message {
	return skills.ContextMessages(messages)
}

func pinnedSkillInstructions(source, kept []db.Message) []db.Message {
	return skills.PinnedInstructions(source, kept)
}
