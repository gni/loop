package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/domain/document"
	"loop/pkg/resources"
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

func LoadSkillsFromDirs(dirs ...string) ([]Skill, error) {
	return loadSkillsFromDirs(os.Stderr, dirs...)
}

// Invalid individual skills are optional resources: warn and keep scanning.
// Directory discovery errors are still returned to the caller.
func loadSkillsFromDirs(warnings io.Writer, dirs ...string) ([]Skill, error) {
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
				fmt.Fprintf(warnings, "Warning: skipping skill %q: exceeds 512 KiB\n", path)
				return nil
			}
			content, exists, err := resources.ReadCachedTextFile(path, 512<<10)
			if err != nil {
				fmt.Fprintf(warnings, "Warning: skipping skill %q: %v\n", path, err)
				return nil
			}
			if !exists {
				return nil
			}

			fm, body, err := document.Frontmatter(content)
			if err != nil {
				fmt.Fprintf(warnings, "Warning: skipping skill %q: %v\n", path, err)
				return nil
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
				fmt.Fprintf(warnings, "Warning: skipping skill %q: duplicate skill name %q\n", path, name)
				return nil
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

func SkillSearchDirs(skillsDir, workspaceRoot string) []string {
	return SkillSearchDirsForWorkspace(skillsDir, workspaceRoot, true)
}

func SkillSearchDirsForWorkspace(skillsDir, workspaceRoot string, trusted bool) []string {
	var dirs []string
	seen := make(map[string]bool)
	var cleanWorkspace string
	if workspaceRoot != "" {
		if absWs, err := filepath.Abs(workspaceRoot); err == nil {
			cleanWorkspace = filepath.Clean(absWs)
		}
	}
	add := func(dir string, projectLocal bool) {
		if projectLocal && !trusted {
			return
		}
		resolved := resolveSkillDir(dir)
		if resolved == "" || seen[resolved] {
			return
		}
		if !trusted && cleanWorkspace != "" && withinRoot(cleanWorkspace, filepath.Clean(resolved)) {
			return
		}
		seen[resolved] = true
		dirs = append(dirs, resolved)
	}

	if skillsDir != "" {
		isRel := !filepath.IsAbs(skillsDir) && !strings.HasPrefix(strings.TrimSpace(skillsDir), "~/")
		add(skillsDir, isRel)
	}
	if trusted && workspaceRoot != "" {
		add(filepath.Join(workspaceRoot, "skills"), true)
		add(filepath.Join(workspaceRoot, ".agents", "skills"), true)
		for _, root := range resources.Ancestors(workspaceRoot) {
			add(filepath.Join(root, ".agents", "skills"), true)
			add(filepath.Join(root, ".loop", "skills"), true)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".agents", "skills"), false)
	}
	return dirs
}
