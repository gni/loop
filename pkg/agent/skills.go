package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
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
