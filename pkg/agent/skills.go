package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"loop/pkg/ui/style"

	"loop/pkg/agent/tool"
)

type Skill = tool.Skill

func ParseFrontmatter(content string) (map[string]string, string) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "---") {
		return nil, content
	}

	rest := strings.TrimPrefix(trimmed, "---")
	if strings.HasPrefix(rest, "\r\n") {
		rest = rest[2:]
	} else if strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	}

	endIdx := -1
	lines := strings.Split(rest, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "---" {
			endIdx = i
			break
		}
	}
	if endIdx == -1 {
		return nil, content
	}

	fmLines := lines[:endIdx]
	bodyLines := lines[endIdx+1:]
	body := strings.Join(bodyLines, "\n")

	fm := make(map[string]string)
	for _, line := range fmLines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kv := strings.SplitN(line, ":", 2)
		if len(kv) == 2 {
			val := strings.TrimSpace(kv[1])
			val = strings.Trim(val, `"'`)
			fm[strings.TrimSpace(kv[0])] = val
		}
	}
	return fm, body
}

func LoadSkillsFromDirs(dirs ...string) ([]Skill, error) {
	var skills []Skill
	seen := make(map[string]bool)

	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if strings.HasPrefix(dir, "~/") {
			homeDir, _ := os.UserHomeDir()
			dir = filepath.Join(homeDir, dir[2:])
		}
		absDir, err := filepath.Abs(dir)
		if err != nil {
			absDir = dir
		}
		if _, err := os.Stat(absDir); os.IsNotExist(err) {
			continue
		}

		_ = filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if strings.ToLower(filepath.Ext(path)) != ".md" {
				return nil
			}

			rel, err := filepath.Rel(absDir, path)
			if err != nil {
				return nil
			}
			relParts := strings.Split(filepath.ToSlash(rel), "/")
			baseName := info.Name()
			baseNoExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))

			// Top-level skill file: skills/my-skill.md (relParts length 1)
			// Or skill package root: skills/my-skill/SKILL.md (relParts length 2)
			// Ignore deeper sub-docs (e.g. skills/my-skill/references/notes.md)
			if len(relParts) > 2 && !strings.EqualFold(baseNoExt, "SKILL") {
				return nil
			}

			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}

			fm, body := ParseFrontmatter(string(data))
			name := ""
			desc := ""
			if fm != nil {
				name = fm["name"]
				desc = fm["description"]
			}

			if name == "" {
				if strings.EqualFold(baseNoExt, "SKILL") || strings.EqualFold(baseNoExt, "README") {
					name = filepath.Base(filepath.Dir(path))
				} else {
					name = baseNoExt
				}
			}

			if desc == "" {
				lines := strings.Split(strings.TrimSpace(body), "\n")
				for _, l := range lines {
					l = strings.TrimSpace(l)
					if l != "" && !strings.HasPrefix(l, "---") {
						desc = strings.TrimPrefix(l, "# ")
						break
					}
				}
				if desc == "" {
					desc = name + " skill guide"
				}
			}

			if !seen[name] {
				seen[name] = true
				skills = append(skills, Skill{
					Name:        name,
					Description: desc,
					Path:        path,
					Content:     strings.TrimSpace(body),
				})
			}
			return nil
		})
	}
	return skills, nil
}

func LoadSkills(skillsDir string) ([]Skill, error) {
	cwd, _ := os.Getwd()
	return LoadSkillsFromDirs(SkillSearchDirs(skillsDir, cwd)...)
}

// SkillSearchDirs resolves every place skills can legitimately live: the
// configured directory plus workspace- and cwd-scoped overrides. The same
// resolution is used at startup and on reload so /skills never reports an empty
// catalog for skills that live in the workspace.
func SkillSearchDirs(skillsDir, workspaceRoot string) []string {
	var dirs []string
	seen := make(map[string]bool)
	add := func(dir string) {
		if dir == "" {
			return
		}
		if strings.HasPrefix(dir, "~/") {
			homeDir, _ := os.UserHomeDir()
			dir = filepath.Join(homeDir, dir[2:])
		}
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}

	add(skillsDir)
	for _, root := range []string{workspaceRoot, "."} {
		if root == "" {
			continue
		}
		add(filepath.Join(root, "skills"))
		add(filepath.Join(root, ".agents", "skills"))
	}
	return dirs
}

// subagentSkillGuidance renders the skill contract. The catalog and the
// no-invention rule apply to every agent because load_skill resolves names from
// the same registry; the delegation rules are emitted only for agents whose
// effective tool catalog actually contains spawn_subagent. Naming an unregistered
// skill is the failure mode this prevents: the registry converts unknown names into
// agent-local skills built from the subagent's system_prompt, so the spawn succeeds
// but the reference instructions are never loaded.
func subagentSkillGuidance(tools []ToolEntry, skills []Skill) string {
	names := uniqueSkillNames(skills)

	var sb strings.Builder
	sb.WriteString("\n\nSubagent skill assignment:\n")
	if len(names) == 0 {
		sb.WriteString("- No registered reference skills are currently installed.\n")
	} else {
		sb.WriteString("- Use skill_names only with these exact registered names: ")
		sb.WriteString(strings.Join(names, ", "))
		sb.WriteString(".\n")
	}
	sb.WriteString("- Use skill_names only for exact registered names. Do not invent reference skill names.\n")
	sb.WriteString("- Unknown names are converted into agent-local skills using the subagent's system_prompt, so spawning continues but the reference instructions are not loaded.\n")
	sb.WriteString("- For a new specialization, define it in the subagent system_prompt or provide inline_skills.\n")

	if hasToolEntry(tools, "create_subagent") || hasToolEntry(tools, "spawn_subagent") {
		sb.WriteString("- Subagents: When the user asks to call or use agents, or to delegate duties, use 'create_subagent' to create specialized agents and delegate tasks to them.\n")
		if hasToolEntry(tools, "remove_subagent") && (hasToolEntry(tools, "audit_subagent") || hasToolEntry(tools, "swarm_audit")) {
			sb.WriteString("- After a delegated task completes, call audit_subagent for the subagent, then remove_subagent to release its context.\n")
		}
	}
	return sb.String()
}

// uniqueSkillNames returns lowercase-deduplicated skill names in stable order so
// the prompt is byte-identical between turns.
func uniqueSkillNames(skills []Skill) []string {
	names := make([]string, 0, len(skills))
	seen := make(map[string]struct{}, len(skills))
	for _, skill := range skills {
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func hasToolEntry(tools []ToolEntry, name string) bool {
	for _, entry := range tools {
		if entry.Name == name {
			return true
		}
	}
	return false
}

func RenderSkills(w io.Writer, skills []Skill, theme style.UITheme) {
	if len(skills) == 0 {
		fmt.Fprintln(w, "No reference skills found.")
		return
	}

	headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	titleStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
	descStyle := style.NewStyle().Foreground(theme.Text)
	pathStyle := style.NewStyle().Foreground(theme.Secondary).Italic(true)

	fmt.Fprintln(w, headerStyle.Render("╭──────────────────────────────────────────────────────────────────────────────────╮"))
	fmt.Fprintln(w, headerStyle.Render("│  AVAILABLE SKILLS (Reference Guides)                                             │"))
	fmt.Fprintln(w, headerStyle.Render("├──────────────────────────────────────────────────────────────────────────────────┤"))

	for _, skill := range skills {
		fmt.Fprintf(w, "  %s - %s\n", titleStyle.Render(skill.Name), descStyle.Render(skill.Description))
		fmt.Fprintf(w, "  %s\n\n", pathStyle.Render(skill.Path))
	}
	fmt.Fprintln(w, headerStyle.Render("╰──────────────────────────────────────────────────────────────────────────────────╯"))
}

// SystemPromptConfig encapsulates all parameters required to generate a
// consistent, deterministic system prompt across single and multi-agent roles.
type SystemPromptConfig struct {
	BaseInstruction string
	WorkspaceRoot   string
	SkillsDir       string
	CompactPrompt   bool
	ActiveAgents    []string
	Skills          []tool.Skill
	AllSkills       []tool.Skill
	MemoryContext   string
	ActiveTasks     []TaskInfo
	UserGuidelines  string
	Tools           []ToolEntry
	// Allowlist restricts which tools the agent may actually call. The catalog
	// must match it, otherwise the model is prompted with tools it cannot use.
	Allowlist []string
}

// ToolEntry is one line of the tool catalog rendered into the system prompt.
type ToolEntry struct {
	Name       string
	Snippet    string
	Guidelines []string
}

// splitInstruction separates the identity preamble from user-authored guidelines
// so both reach the model. Previously everything after "Guidelines:" was discarded.
func splitInstruction(instruction string) (identity, guidelines string) {
	const marker = "Guidelines:"
	if idx := strings.Index(instruction, marker); idx != -1 {
		identity = strings.TrimSpace(instruction[:idx])
		guidelines = strings.TrimSpace(instruction[idx+len(marker):])
		return identity, strings.TrimLeft(guidelines, "\n")
	}
	return strings.TrimSpace(instruction), ""
}

// BuildSystemPrompt constructs the complete system prompt instructions, guidelines,
// skill catalog, and multi-agent topology guidance using structured XML sections.
func BuildSystemPrompt(cfg SystemPromptConfig) string {
	workspaceRoot := cfg.WorkspaceRoot
	if workspaceRoot == "" {
		workspaceRoot = "."
	}

	skillsDir := cfg.SkillsDir
	if skillsDir == "" {
		skillsDir = "skills"
	}

	skillsForGuidance := cfg.Skills
	if len(cfg.AllSkills) > 0 {
		skillsForGuidance = cfg.AllSkills
	}

	// User guidelines must survive: they were previously truncated away, which is
	// why instructions written in system_instruction never reached the model.
	baseInstruction, userGuidelines := splitInstruction(cfg.BaseInstruction)
	baseInstruction = strings.TrimSpace(baseInstruction)
	if userGuidelines == "" {
		userGuidelines = cfg.UserGuidelines
	}
	if baseInstruction == "" {
		baseInstruction = "You are loop, an elite autonomous software engineering harness. You solve engineering tasks with senior craft, architectural rigor, and direct working deliverables."
	}

	var sb strings.Builder

	// 1. Preamble section
	sb.WriteString(baseInstruction)

	// 2. Tools section, generated from the registry so plugins and MCP tools are
	// never omitted from the model's view of what it can call.
	tools := filterToolEntries(cfg.Tools, cfg.Allowlist)
	sb.WriteString("\n\n<tools>\n")
	for _, entry := range tools {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", entry.Name, entry.Snippet))
	}
	for _, name := range cfg.ActiveAgents {
		sb.WriteString(fmt.Sprintf("- subagent__%s: Delegate task to specialized subagent '%s'\n", name, name))
	}
	sb.WriteString("</tools>\n\n")

	// 3. Rules section: core rules, then per-tool guidelines, then user guidelines.
	sb.WriteString("<rules>\n")
	sb.WriteString(fmt.Sprintf("- Workspace root: `%s`. Any relative file paths resolve relative to this directory.\n", workspaceRoot))
	sb.WriteString("- Actions over talk: Implement code on disk directly using write and edit tools. Deliver complete working code.\n")
	sb.WriteString("- Read intent before acting: answer conversational prompts (greetings, questions, clarifications) directly; only call tools when the message asks for work on files or commands.\n")
	for _, entry := range tools {
		for _, rule := range entry.Guidelines {
			sb.WriteString(fmt.Sprintf("- %s\n", rule))
		}
	}
	sb.WriteString("- Background Processes: When asked to run a command or service in the background, use the 'bash' tool with \"background\": true.\n")
	sb.WriteString("- Be concise and direct in your responses.\n")
	if userGuidelines != "" {
		sb.WriteString("\n<User Guidelines>\n")
		sb.WriteString(userGuidelines)
		sb.WriteString("\n</User Guidelines>")
	}
	sb.WriteString("</rules>")

	// 4. Skills section
	if len(cfg.Skills) > 0 {
		sb.WriteString("\n\n<skills>\n")
		sb.WriteString(fmt.Sprintf("Installed reference skills are stored in `%s`. Use the 'load_skill' tool to read detailed instructions:\n", skillsDir))
		for _, s := range cfg.Skills {
			sb.WriteString(fmt.Sprintf("- name: %s\n  description: %s\n", s.Name, s.Description))
		}
		sb.WriteString("</skills>")
	}

	sb.WriteString(subagentSkillGuidance(tools, skillsForGuidance))

	// 5. Swarm info if subagents active (in non-compact mode)
	if !cfg.CompactPrompt && len(cfg.ActiveAgents) > 0 {
		sb.WriteString(fmt.Sprintf("\n\n<subagents>\nActive subagents: %s.\nDelegate subtasks by calling 'subagent__<name>' with the task prompt.\n</subagents>", strings.Join(cfg.ActiveAgents, ", ")))
	}

	if cfg.MemoryContext != "" {
		sb.WriteString(cfg.MemoryContext)
	}

	// 6. Background tasks section if running tasks exist
	if len(cfg.ActiveTasks) > 0 {
		sb.WriteString("\n\n<background_tasks>\n")
		sb.WriteString("Active running background tasks in this session:\n")
		for _, t := range cfg.ActiveTasks {
			sb.WriteString(fmt.Sprintf("- %s: status=%s command=`%s`\n", t.ID, t.Status, t.Command))
		}
		sb.WriteString("Use 'task_status' with task_id to inspect output or 'task_kill' to terminate a task.\n")
		sb.WriteString("</background_tasks>")
	}

	// 7. CWD section
	sb.WriteString(fmt.Sprintf("\n\n<cwd>\n%s\n</cwd>", workspaceRoot))

	return sb.String()
}

func (a *Agent) GetSystemPrompt() string { return a.GetSystemPromptFor(nil) }

// GetSystemPromptFor builds the prompt for a specific tool allowlist so the
// advertised tool catalog matches the tools the agent can actually execute.
func (a *Agent) GetSystemPromptFor(allowlist []string) string {
	var activeAgents []string
	a.SpawnedAgentsMu.RLock()
	for name := range a.SpawnedAgents {
		activeAgents = append(activeAgents, name)
	}
	a.SpawnedAgentsMu.RUnlock()
	sort.Strings(activeAgents)

	skillsDir := "skills"
	compact := false
	instruction := ""
	if a.Config != nil {
		skillsDir = a.Config.SkillsDir
		compact = a.Config.CompactPrompt
		instruction = a.Config.SystemInstruction
	}

	var activeTasks []TaskInfo
	for _, t := range a.ListTasks() {
		if t.Status == "running" {
			activeTasks = append(activeTasks, t)
		}
	}

	return BuildSystemPrompt(SystemPromptConfig{
		BaseInstruction: instruction,
		WorkspaceRoot:   a.WorkspaceRoot,
		SkillsDir:       skillsDir,
		CompactPrompt:   compact,
		ActiveAgents:    activeAgents,
		Skills:          a.ActiveSkills,
		MemoryContext:   a.LoadMemoryContext(),
		ActiveTasks:     activeTasks,
		Allowlist:       allowlist,
		Tools:           promptToolEntries(a.Registry),
	})
}

func filterToolEntries(entries []ToolEntry, allowlist []string) []ToolEntry {
	if len(allowlist) == 0 {
		return entries
	}
	allowed := make(map[string]bool, len(allowlist))
	for _, name := range allowlist {
		allowed[name] = true
	}
	out := make([]ToolEntry, 0, len(entries))
	for _, entry := range entries {
		if allowed[entry.Name] || strings.HasPrefix(entry.Name, "mcp__") {
			out = append(out, entry)
		}
	}
	return out
}

// promptToolEntries renders the tool catalog from the live registry, so plugin
// and MCP tools are visible to the model instead of being hardcoded.
func promptToolEntries(registry *tool.ToolRegistry) []ToolEntry {
	if registry == nil {
		return nil
	}
	executors := registry.GetAllExecutors()
	entries := make([]ToolEntry, 0, len(executors))
	for name, executor := range executors {
		entries = append(entries, ToolEntry{
			Name:       name,
			Snippet:    tool.GetPromptSnippet(executor),
			Guidelines: tool.GetPromptGuidelines(executor),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}
