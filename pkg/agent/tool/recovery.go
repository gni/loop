package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/domain/diagnostic"
)

func RecoveryFor(name, arguments string, err error) *diagnostic.Recovery {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	var failure *diagnostic.Error
	target := filetool.ExtractFilePath(arguments)
	code := diagnostic.Code("")
	line := 0
	if errors.As(err, &failure) {
		code, line = failure.Code, failure.Line
		if failure.Target != "" {
			target = failure.Target
		}
	} else if errors.Is(err, os.ErrNotExist) {
		code = diagnostic.MissingPath
	} else if errors.Is(err, os.ErrPermission) {
		code = diagnostic.PermissionDenied
	}
	recovery := &diagnostic.Recovery{Code: code}
	var next map[string]any
	switch code {
	case diagnostic.ReadRequired, diagnostic.RevisionChanged:
		recovery.Instruction = "Read the current file before making a targeted edit. Base the replacement on that read; do not bypass the revision check with write or a shell overwrite."
		recovery.Tool, next = "read", map[string]any{"path": target}
	case diagnostic.EditNotFound:
		recovery.Instruction = "No edit was committed. Read the current matching region, copy a small unique block, and revise oldText. Do not repeat the stale block or replace the whole file to bypass the mismatch."
		recovery.Tool, next = "read", map[string]any{"path": target}
		if line > 0 {
			next["offset"], next["limit"] = max(1, line-5), 40
		}
	case diagnostic.AmbiguousEdit:
		recovery.Instruction = "No edit was committed. Include surrounding lines that identify one intended occurrence; inspect the matching region before retrying."
		recovery.Tool, next = "read", map[string]any{"path": target}
	case diagnostic.MissingPath:
		recovery.Instruction = "Locate the intended path before trying again. A missing path does not establish that a new file should be created."
		if target != "" {
			recovery.Tool, next = "find", map[string]any{"pattern": filepath.Base(target), "limit": 20}
		}
	case diagnostic.PermissionDenied:
		recovery.Instruction = "The filesystem denied this operation. Check the target and report the permission issue; repeating the call will not grant access."
	default:
		return nil
	}
	if target == "" && recovery.Tool == "read" {
		recovery.Tool, next = "", nil
	}
	if next != nil {
		recovery.Arguments, _ = json.Marshal(next)
	}
	return recovery
}
