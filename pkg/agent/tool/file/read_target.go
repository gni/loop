package file

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"

	"loop/pkg/domain/ignore"
	"loop/pkg/domain/limits"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/storage"
)

func resolveReadTarget(ctx domaintool.AgentContext, target string) (string, os.FileInfo, string, error) {
	path, err := safeReadPath(ctx, target)
	if err != nil {
		return "", nil, "", err
	}
	root := ctx.GetWorkspaceRoot()
	if ignore.HasComponent(path, root, false) {
		return path, nil, "", fmt.Errorf("cannot read: path '%s' is inside a dependency or ignored folder (venv, node_modules, etc.)", target)
	}
	info, err := os.Stat(path)
	header := ""
	if err != nil {
		resolved, resolvedInfo, notice, found := resolveMissingReadTarget(path, target, root)
		if !found {
			return path, nil, "", fmt.Errorf("failed to read file info: %w", err)
		}
		path, info, header = resolved, resolvedInfo, notice
	}
	if info.IsDir() {
		if resolved, resolvedInfo, notice, found := resolveDirEntrypoint(path, target, root); found {
			path, info, header = resolved, resolvedInfo, notice
		}
	}
	// Fallback extensions and entrypoints can resolve through another symlink.
	path, err = safeReadPath(ctx, path)
	if err != nil {
		return "", nil, "", err
	}
	if ignore.HasComponent(path, root, false) {
		return path, nil, "", fmt.Errorf("cannot read ignored path %s", target)
	}
	return path, info, header, nil
}

// ReadResultCurrent checks a cached read without observing source for the model.
// Path resolution and byte limits match the actual read, including fallbacks.
func (t *readTool) ReadResultCurrent(ctx context.Context, exec domaintool.AgentContext, arguments, target, revision string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	path, info, _, err := resolveReadTarget(exec, ExtractFilePath(arguments))
	if err != nil || path != target || !info.Mode().IsRegular() || info.Size() > int64(limits.For(exec).MaxReadFileBytes) {
		return false, ctx.Err()
	}
	unlock, err := LockPathContext(ctx, path)
	if err != nil {
		return false, err
	}
	defer unlock()
	data, err := storage.ReadFileContext(ctx, path, int64(limits.For(exec).MaxReadFileBytes))
	if err != nil {
		return false, ctx.Err()
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)) == revision, nil
}
