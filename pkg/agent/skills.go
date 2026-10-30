package agent

import (
	"fmt"
	"io"
	"loop/pkg/domain/document"
	"loop/pkg/resources"
	"loop/pkg/storage"
	"os"
	"path/filepath"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/ui/style"
)

type Skill = tool.Skill

func resolveSkillDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	if strings.HasPrefix(dir, "~/") {
		if homeDir, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(homeDir, dir[2:])
		}
	}
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

func ParseFrontmatter(content string) (map[string]string, string) {
	metadata, body, err := document.Frontmatter(content)
	if err != nil {
		return nil, content
	}
	return metadata, body
}

func LoadSkillsFromDirs(dirs ...string) ([]Skill, error) {
	var skills []Skill
	seen := make(map[string]bool)
	visited := make(map[string]bool)

	for _, rawDir := range dirs {
		absDir := resolveSkillDir(rawDir)
		if absDir == "" {
			continue
		}
		if _, err := os.Stat(absDir); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("inspect skills directory %q: %w", absDir, err)
		}

		err := filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return fmt.Errorf("visit skill path %q: %w", path, err)
			}
			if info.IsDir() || strings.ToLower(filepath.Ext(path)) != ".md" {
				return nil
			}

			if visited[path] {
				return nil
			}
			visited[path] = true
			rel, err := filepath.Rel(absDir, path)
			if err != nil {
				return fmt.Errorf("resolve skill path %q: %w", path, err)
			}
			relParts := strings.Split(filepath.ToSlash(rel), "/")
			baseName := info.Name()
			baseNoExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))

			if len(relParts) > 2 && !strings.EqualFold(baseNoExt, "SKILL") {
				return nil
			}

			if info.Size() > 512<<10 {
				return fmt.Errorf("skill %s exceeds 512 KiB", path)
			}
			data, err := storage.ReadFile(path, 512<<10)
			if err != nil {
				return fmt.Errorf("read skill %q: %w", path, err)
			}

			fm, body, err := document.Frontmatter(string(data))
			if err != nil {
				return fmt.Errorf("skill %s: %w", path, err)
			}
			var name, desc string
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
				for _, l := range strings.Split(strings.TrimSpace(body), "\n") {
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

			key := strings.ToLower(name)
			if seen[key] {
				return fmt.Errorf("duplicate skill name %q at %s", name, path)
			}
			seen[key] = true
			skills = append(skills, Skill{
				Name:                   name,
				Description:            desc,
				Path:                   path,
				Content:                strings.TrimSpace(body),
				DisableModelInvocation: fm["disable-model-invocation"] == "true",
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return skills, nil
}

func LoadSkills(skillsDir string) ([]Skill, error) {
	cwd, _ := os.Getwd()
	return LoadSkillsFromDirs(SkillSearchDirs(skillsDir, cwd)...)
}

func SkillSearchDirs(skillsDir, workspaceRoot string) []string {
	var dirs []string
	seen := make(map[string]bool)
	add := func(dir string) {
		resolved := resolveSkillDir(dir)
		if resolved != "" && !seen[resolved] {
			seen[resolved] = true
			dirs = append(dirs, resolved)
		}
	}

	add(skillsDir)
	for _, root := range []string{workspaceRoot} {
		if root == "" {
			continue
		}
		add(filepath.Join(root, "skills"))
		add(filepath.Join(root, ".agents", "skills"))
	}
	for _, root := range resources.Ancestors(workspaceRoot) {
		add(filepath.Join(root, ".agents", "skills"))
		add(filepath.Join(root, ".loop", "skills"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".agents", "skills"))
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
		fmt.Fprintf(w, "  %s : %s\n", titleStyle.Render(skill.Name), descStyle.Render(skill.Description))
		fmt.Fprintf(w, "  %s\n\n", pathStyle.Render(skill.Path))
	}
	fmt.Fprintln(w, headerStyle.Render("╰──────────────────────────────────────────────────────────────────────────────────╯"))
}
