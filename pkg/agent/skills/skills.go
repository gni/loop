package skills

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/domain/document"
	"loop/pkg/resources"
)

type Skill = tool.Skill

func ResolveDir(dir string) string {
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

func LoadFromDirs(dirs ...string) ([]Skill, error) {
	return LoadFromDirsWithWarnings(os.Stderr, dirs...)
}

// Invalid individual skills are optional resources: warn and keep scanning.
// Directory discovery errors are still returned to the caller.
func LoadFromDirsWithWarnings(warnings io.Writer, dirs ...string) ([]Skill, error) {
	var skills []Skill
	seen := make(map[string]bool)
	visited := make(map[string]bool)

	for _, rawDir := range dirs {
		absDir := ResolveDir(rawDir)
		if absDir == "" {
			continue
		}
		info, err := os.Stat(absDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("inspect skills directory %q: %w", absDir, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("skills path %q is not a directory", absDir)
		}
		absDir, err = filepath.EvalSymlinks(absDir)
		if err != nil {
			return nil, fmt.Errorf("resolve skills directory %q: %w", rawDir, err)
		}

		addSkill := func(path string) {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				fmt.Fprintf(warnings, "Warning: skipping skill %q: %v\n", path, err)
				return
			}
			if visited[resolved] {
				return
			}
			visited[resolved] = true
			skill, ok := readSkill(path, warnings)
			if !ok {
				return
			}
			key := strings.ToLower(skill.Name)
			if seen[key] {
				fmt.Fprintf(warnings, "Warning: skipping skill %q: duplicate skill name %q\n", path, skill.Name)
				return
			}
			seen[key] = true
			skills = append(skills, skill)
		}
		err = filepath.WalkDir(absDir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return fmt.Errorf("visit skill path %q: %w", path, err)
			}
			isDir := entry.IsDir()
			if entry.Type()&os.ModeSymlink != 0 {
				info, err := os.Stat(path)
				if err != nil {
					fmt.Fprintf(warnings, "Warning: skipping skill link %q: %v\n", path, err)
					return nil
				}
				isDir = info.IsDir()
			}
			if isDir {
				entries, err := os.ReadDir(path)
				if err != nil {
					return fmt.Errorf("list skill directory %q: %w", path, err)
				}
				for _, child := range entries {
					if !child.IsDir() && strings.EqualFold(child.Name(), "SKILL.md") {
						addSkill(filepath.Join(path, child.Name()))
						// README and supporting documents belong to this skill.
						if entry.IsDir() {
							return filepath.SkipDir
						}
						return nil
					}
				}
				// Inspect linked packages, but do not recursively follow linked
				// collections or ancestors: those may form cycles.
				return nil
			}
			// Keep legacy <skills>/<name>.md guides. Nested supporting files
			// are not independent skills; nested SKILL.md packages are found above.
			if filepath.Dir(path) != absDir || !strings.EqualFold(filepath.Ext(path), ".md") || strings.EqualFold(entry.Name(), "README.md") {
				return nil
			}
			addSkill(path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return skills, nil
}

func readSkill(path string, warnings io.Writer) (Skill, bool) {
	content, exists, err := resources.ReadCachedTextFile(path, 512<<10)
	if err != nil {
		fmt.Fprintf(warnings, "Warning: skipping skill %q: %v\n", path, err)
		return Skill{}, false
	}
	if !exists {
		return Skill{}, false
	}
	fm, body, err := document.Frontmatter(content)
	if err != nil {
		fmt.Fprintf(warnings, "Warning: skipping skill %q: %v\n", path, err)
		return Skill{}, false
	}
	body = strings.TrimSpace(body)
	if body == "" {
		fmt.Fprintf(warnings, "Warning: skipping skill %q: no instructions\n", path)
		return Skill{}, false
	}
	name, desc := strings.TrimSpace(fm["name"]), strings.TrimSpace(fm["description"])
	if name == "" {
		base := filepath.Base(path)
		if strings.EqualFold(base, "SKILL.md") {
			name = filepath.Base(filepath.Dir(path))
		} else {
			name = strings.TrimSuffix(base, filepath.Ext(base))
		}
	}
	if desc == "" {
		for line := range strings.SplitSeq(body, "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "---") {
				desc = strings.TrimPrefix(line, "# ")
				break
			}
		}
		if desc == "" {
			desc = name + " skill guide"
		}
	}
	return Skill{
		Name:                   name,
		Description:            desc,
		Path:                   path,
		Content:                body,
		DisableModelInvocation: strings.EqualFold(strings.TrimSpace(fm["disable-model-invocation"]), "true"),
	}, true
}

func SearchDirs(skillsDir, workspaceRoot string) []string {
	return SearchDirsForWorkspace(skillsDir, workspaceRoot, true)
}

func SearchDirsForWorkspace(skillsDir, workspaceRoot string, trusted bool) []string {
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
		resolved := ResolveDir(dir)
		if resolved == "" || seen[resolved] {
			return
		}
		if !trusted && cleanWorkspace != "" && withinRoot(cleanWorkspace, filepath.Clean(resolved)) {
			return
		}
		seen[resolved] = true
		dirs = append(dirs, resolved)
	}

	if skillsDir = strings.TrimSpace(skillsDir); skillsDir != "" {
		isRel := !filepath.IsAbs(skillsDir) && !strings.HasPrefix(strings.TrimSpace(skillsDir), "~/")
		if isRel && workspaceRoot != "" {
			skillsDir = filepath.Join(workspaceRoot, skillsDir)
		}
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
		add(filepath.Join(home, ".loop", "skills"), false)
	}
	return dirs
}

func withinRoot(cleanRoot, target string) bool {
	rel, err := filepath.Rel(cleanRoot, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
