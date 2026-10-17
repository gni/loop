package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SafePath canonicalizes paths and enforces workspace sandbox boundary containment.
func (a *Agent) SafePath(inputPath string) (string, error) {
	if inputPath == "" {
		return a.WorkspaceRoot, nil
	}

	target := inputPath
	if !filepath.IsAbs(target) {
		target = filepath.Join(a.WorkspaceRoot, target)
	}

	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}

	cleanRoot := filepath.Clean(a.WorkspaceRoot)
	cleanTarget := filepath.Clean(absTarget)

	if cleanTarget == cleanRoot {
		return cleanTarget, nil
	}

	// Resilient fallback: the model sometimes repeats the workspace root folder name
	// (e.g. 'tests/fastapi_boilerplate' when the root is already '/workspace/tests').
	// Resolve that before any containment check so legitimate paths are not rejected.
	cleanTarget = a.resolveRepeatedRootName(cleanRoot, cleanTarget, inputPath)

	if cleanTarget == cleanRoot {
		return cleanTarget, nil
	}

	// Surgical security checks: block modifying active configuration file or session databases
	if a.ConfigPath != "" {
		absConfig, errConfig := filepath.Abs(a.ConfigPath)
		if errConfig == nil {
			cleanConfig := filepath.Clean(absConfig)
			if cleanTarget == cleanConfig {
				return "", fmt.Errorf("security violation: modifying the active configuration file is not allowed")
			}
			cleanSessionsDir := filepath.Clean(filepath.Join(filepath.Dir(absConfig), "sessions"))
			if cleanTarget == cleanSessionsDir || strings.HasPrefix(cleanTarget, cleanSessionsDir+string(filepath.Separator)) {
				return "", fmt.Errorf("security violation: modifying session database files is not allowed")
			}
		}
	}

	// Surgical allowlist: allow writing to global memory files
	home, err := os.UserHomeDir()
	if err == nil {
		globalLoop := filepath.Clean(filepath.Join(home, ".loop", "LOOP.md"))
		if cleanTarget == globalLoop {
			return cleanTarget, nil
		}
	}

	prefix := cleanRoot
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}

	if !withinRoot(cleanRoot, cleanTarget) {
		if strings.TrimSpace(inputPath) == ".." {
			return "", fmt.Errorf("path '..' is outside workspace root '%s'. Workspace root is the top-level directory", a.WorkspaceRoot)
		}
		return "", fmt.Errorf("security violation: path '%s' escapes workspace root '%s'", inputPath, a.WorkspaceRoot)
	}

	// Resolve the closest existing ancestor, including when several parents do not
	// exist yet. Checking only the immediate parent misses escaping symlinks.
	evalRoot, err := filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		return "", fmt.Errorf("cannot resolve workspace root: %w", err)
	}
	ancestor := cleanTarget
	var evalTarget string
	for {
		realAncestor, resolveErr := filepath.EvalSymlinks(ancestor)
		if resolveErr == nil {
			rel, relErr := filepath.Rel(ancestor, cleanTarget)
			if relErr != nil {
				return "", fmt.Errorf("cannot resolve path '%s': %w", inputPath, relErr)
			}
			evalTarget = filepath.Clean(filepath.Join(realAncestor, rel))
			break
		}
		if !os.IsNotExist(resolveErr) {
			return "", fmt.Errorf("cannot resolve path '%s': %w", inputPath, resolveErr)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("cannot resolve existing ancestor of '%s'", inputPath)
		}
		ancestor = parent
	}
	if !withinRoot(evalRoot, evalTarget) {
		return "", fmt.Errorf("security violation: path '%s' resolves via symlink outside workspace root '%s'", inputPath, a.WorkspaceRoot)
	}

	return cleanTarget, nil
}

func withinRoot(cleanRoot, target string) bool {
	if target == cleanRoot {
		return true
	}
	prefix := cleanRoot
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(target, prefix)
}

// resolveRepeatedRootName repairs inputs that repeat the workspace root folder name as their
// first segment (e.g. 'tests/fastapi_boilerplate' when the root is already '/workspace/tests').
// It rewrites the path only when the original form is absent on disk and the de-duplicated form
// exists, so genuine paths that happen to start with the root name are never mangled.
func (a *Agent) resolveRepeatedRootName(cleanRoot, cleanTarget, inputPath string) string {
	if cleanTarget == cleanRoot {
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
