package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"loop/pkg/resources"
)

func isCaseInsensitiveFS() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "windows"
}

func samePath(a, b string) bool {
	if isCaseInsensitiveFS() {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func hasPathPrefix(target, prefix string) bool {
	if isCaseInsensitiveFS() {
		return strings.HasPrefix(strings.ToLower(target), strings.ToLower(prefix))
	}
	return strings.HasPrefix(target, prefix)
}

func firstSubpathSegment(root, target string) string {
	rootParts := strings.Split(strings.TrimSuffix(filepath.ToSlash(filepath.Clean(root)), "/"), "/")
	targetParts := strings.Split(strings.TrimSuffix(filepath.ToSlash(filepath.Clean(target)), "/"), "/")
	if len(targetParts) <= len(rootParts) {
		return "."
	}
	return targetParts[len(rootParts)]
}

func resolveExistingAncestorSymlinks(cleanTarget, inputPath string) (string, error) {
	current := cleanTarget
	for hops := 0; hops < 40; hops++ {
		ancestor := current
		followedLink := false
		for {
			realAncestor, resolveErr := filepath.EvalSymlinks(ancestor)
			if resolveErr == nil {
				rel, relErr := filepath.Rel(ancestor, current)
				if relErr != nil {
					return "", fmt.Errorf("cannot resolve path '%s': %w", inputPath, relErr)
				}
				return filepath.Clean(filepath.Join(realAncestor, rel)), nil
			}
			if !os.IsNotExist(resolveErr) {
				return "", fmt.Errorf("cannot resolve path '%s': %w", inputPath, resolveErr)
			}
			if info, lstatErr := os.Lstat(ancestor); lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
				linkTarget, readErr := os.Readlink(ancestor)
				if readErr != nil {
					return "", fmt.Errorf("cannot resolve symlink '%s': %w", inputPath, readErr)
				}
				rel, relErr := filepath.Rel(ancestor, current)
				if relErr != nil {
					return "", fmt.Errorf("cannot resolve path '%s': %w", inputPath, relErr)
				}
				if !filepath.IsAbs(linkTarget) {
					linkTarget = filepath.Join(filepath.Dir(ancestor), linkTarget)
				}
				current = filepath.Clean(filepath.Join(linkTarget, rel))
				followedLink = true
				break
			}
			parent := filepath.Dir(ancestor)
			if parent == ancestor {
				return "", fmt.Errorf("cannot resolve existing ancestor of '%s'", inputPath)
			}
			ancestor = parent
		}
		if !followedLink {
			break
		}
	}
	return "", fmt.Errorf("too many symlink levels resolving '%s'", inputPath)
}

func privateRuntimeConfigName(name string) bool {
	name = strings.ToLower(name)
	switch name {
	case "auth.json", "config.json", "mcp.json", "providers.json", "packages.json", ".config-recovery.json":
		return true
	}
	return strings.HasPrefix(name, ".loop-config-")
}

func (a *Agent) isProtectedRuntimePath(candidate string) error {
	if a.ConfigPath != "" {
		if absConfig, err := filepath.Abs(a.ConfigPath); err == nil {
			cleanConfig := filepath.Clean(absConfig)
			if evalCfg, evalErr := resolveExistingAncestorSymlinks(cleanConfig, cleanConfig); evalErr == nil {
				if samePath(candidate, evalCfg) || samePath(candidate, evalCfg+".lock") {
					return fmt.Errorf("security violation: accessing the active configuration file is not allowed")
				}
			}
			if samePath(candidate, cleanConfig) || samePath(candidate, cleanConfig+".lock") {
				return fmt.Errorf("security violation: accessing the active configuration file is not allowed")
			}
		}
	}

	var agentDirs []string
	agentDir := resources.AgentDirectory(a.ConfigPath)
	if agentDir != "" {
		if absDir, err := filepath.Abs(agentDir); err == nil {
			cleanDir := filepath.Clean(absDir)
			agentDirs = append(agentDirs, cleanDir)
			if evalDir, evalErr := resolveExistingAncestorSymlinks(cleanDir, cleanDir); evalErr == nil {
				agentDirs = append(agentDirs, filepath.Clean(evalDir))
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		homeLoop := filepath.Clean(filepath.Join(home, ".loop"))
		agentDirs = append(agentDirs, homeLoop)
		if evalHomeLoop, evalErr := resolveExistingAncestorSymlinks(homeLoop, homeLoop); evalErr == nil {
			agentDirs = append(agentDirs, filepath.Clean(evalHomeLoop))
		}
	}

	for _, dir := range agentDirs {
		if !withinRoot(dir, candidate) {
			continue
		}
		top := firstSubpathSegment(dir, candidate)
		topCompare := top
		if isCaseInsensitiveFS() {
			topCompare = strings.ToLower(top)
		}
		switch topCompare {
		case "sessions":
			return fmt.Errorf("security violation: accessing session database files is not allowed")
		case "agents", "trust", "recovery":
			return fmt.Errorf("security violation: accessing runtime security or agent state is not allowed")
		}
		if privateRuntimeConfigName(top) {
			return fmt.Errorf("security violation: accessing runtime credentials or configuration is not allowed")
		}
		if strings.HasSuffix(topCompare, ".lock") {
			return fmt.Errorf("security violation: accessing runtime lock files is not allowed")
		}
	}

	parentBase := filepath.Base(filepath.Dir(candidate))
	base := filepath.Base(candidate)
	if samePath(parentBase, ".loop") {
		if privateRuntimeConfigName(base) {
			return fmt.Errorf("security violation: accessing protected .loop configuration is not allowed")
		}
		baseCompare := base
		if isCaseInsensitiveFS() {
			baseCompare = strings.ToLower(base)
		}
		if strings.HasSuffix(baseCompare, ".lock") {
			return fmt.Errorf("security violation: accessing .loop lock files is not allowed")
		}
	}
	return nil
}

func (a *Agent) checkProtectedTarget(cleanTarget, inputPath string) error {
	if err := a.isProtectedRuntimePath(cleanTarget); err != nil {
		return err
	}
	if evalTarget, evalErr := resolveExistingAncestorSymlinks(cleanTarget, inputPath); evalErr == nil {
		if err := a.isProtectedRuntimePath(evalTarget); err != nil {
			return err
		}
	}
	return nil
}

// SafePath canonicalizes paths and enforces workspace sandbox boundary containment.
func (a *Agent) SafePath(inputPath string) (string, error) {
	target := inputPath
	if target == "" {
		if a.WorkspaceRoot == "" {
			return "", nil
		}
		target = a.WorkspaceRoot
	} else if !filepath.IsAbs(target) {
		target = filepath.Join(a.WorkspaceRoot, target)
	}

	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}

	cleanRoot := filepath.Clean(a.WorkspaceRoot)
	cleanTarget := filepath.Clean(absTarget)

	if samePath(cleanTarget, cleanRoot) {
		if err := a.checkProtectedTarget(cleanTarget, inputPath); err != nil {
			return "", err
		}
		return cleanTarget, nil
	}

	// Resilient fallback: the model sometimes repeats the workspace root folder name
	// (e.g. 'tests/fastapi_boilerplate' when the root is already '/workspace/tests').
	// Resolve that before any containment check so legitimate paths are not rejected.
	cleanTarget = a.resolveRepeatedRootName(cleanRoot, cleanTarget, inputPath)

	if samePath(cleanTarget, cleanRoot) {
		if err := a.checkProtectedTarget(cleanTarget, inputPath); err != nil {
			return "", err
		}
		return cleanTarget, nil
	}

	evalTarget, err := resolveExistingAncestorSymlinks(cleanTarget, inputPath)
	if err != nil {
		return "", err
	}

	if err := a.isProtectedRuntimePath(cleanTarget); err != nil {
		return "", err
	}
	if err := a.isProtectedRuntimePath(evalTarget); err != nil {
		return "", err
	}

	// Surgical allowlist: allow writing to global LOOP.md only when its resolved
	// target genuinely stays at ~/.loop/LOOP.md (never via escaping symlinks).
	if home, homeErr := os.UserHomeDir(); homeErr == nil && home != "" {
		globalLoopDir := filepath.Clean(filepath.Join(home, ".loop"))
		globalLoop := filepath.Join(globalLoopDir, "LOOP.md")
		if samePath(cleanTarget, globalLoop) {
			evalLoopDir := globalLoopDir
			if resolvedDir, evalErr := resolveExistingAncestorSymlinks(globalLoopDir, globalLoopDir); evalErr == nil {
				evalLoopDir = resolvedDir
			}
			if !samePath(evalTarget, filepath.Join(evalLoopDir, "LOOP.md")) {
				return "", fmt.Errorf("security violation: global LOOP.md resolves via symlink outside %s", globalLoopDir)
			}
			return cleanTarget, nil
		}
	}

	evalRoot, err := filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		return "", fmt.Errorf("cannot resolve workspace root: %w", err)
	}

	if !withinRoot(cleanRoot, cleanTarget) && !withinRoot(evalRoot, cleanTarget) {
		if strings.TrimSpace(inputPath) == ".." {
			return "", fmt.Errorf("path '..' is outside workspace root '%s'. Workspace root is the top-level directory", a.WorkspaceRoot)
		}
		return "", fmt.Errorf("security violation: path '%s' escapes workspace root '%s'", inputPath, a.WorkspaceRoot)
	}

	if !withinRoot(evalRoot, evalTarget) {
		return "", fmt.Errorf("security violation: path '%s' resolves via symlink outside workspace root '%s'", inputPath, a.WorkspaceRoot)
	}

	return cleanTarget, nil
}

// SafeReadPath also permits supporting files inside installed skill packages.
// This does not grant mutation access or allow links to escape a skill folder.
func (a *Agent) SafeReadPath(inputPath string) (string, error) {
	path, pathErr := a.SafePath(inputPath)
	if pathErr == nil || !filepath.IsAbs(inputPath) {
		return path, pathErr
	}
	cleanTarget := filepath.Clean(inputPath)
	evalTarget, err := filepath.EvalSymlinks(cleanTarget)
	if err != nil {
		return "", pathErr
	}
	if err := a.isProtectedRuntimePath(cleanTarget); err != nil {
		return "", err
	}
	if err := a.isProtectedRuntimePath(evalTarget); err != nil {
		return "", err
	}
	for _, skill := range a.GetActiveSkills() {
		if skill.Path == "" {
			continue
		}
		root := filepath.Dir(skill.Path)
		evalRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if (withinRoot(root, cleanTarget) || withinRoot(evalRoot, cleanTarget)) && withinRoot(evalRoot, evalTarget) {
			return cleanTarget, nil
		}
	}
	return "", pathErr
}

func withinRoot(cleanRoot, target string) bool {
	if samePath(target, cleanRoot) {
		return true
	}
	prefix := cleanRoot
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return hasPathPrefix(target, prefix)
}

// resolveRepeatedRootName repairs inputs that repeat the workspace root folder name as their
// first segment (e.g. 'tests/fastapi_boilerplate' when the root is already '/workspace/tests').
// It rewrites the path only when the original form is absent on disk and the de-duplicated form
// exists, so genuine paths that happen to start with the root name are never mangled.
func (a *Agent) resolveRepeatedRootName(cleanRoot, cleanTarget, inputPath string) string {
	if samePath(cleanTarget, cleanRoot) {
		return cleanTarget
	}

	rootBase := filepath.Base(cleanRoot)
	if rootBase == "." || rootBase == string(filepath.Separator) {
		return cleanTarget
	}

	slashInput := filepath.ToSlash(inputPath)
	if !strings.HasPrefix(slashInput, rootBase+"/") && slashInput != rootBase {
		return cleanTarget
	}

	if _, err := os.Stat(cleanTarget); err == nil {
		return cleanTarget
	}

	// The input is exactly the root name and no literal subdirectory of that name exists:
	// it refers to the workspace root itself.
	if slashInput == rootBase {
		return cleanRoot
	}

	stripped := strings.TrimPrefix(slashInput, rootBase+"/")
	if stripped == "" || stripped == "." {
		return cleanRoot
	}

	altTarget := filepath.Clean(filepath.Join(cleanRoot, stripped))
	// Never let the rewrite escape the workspace root.
	if !withinRoot(cleanRoot, altTarget) {
		return cleanTarget
	}
	if _, err := os.Stat(altTarget); err == nil {
		return altTarget
	}

	return cleanTarget
}
