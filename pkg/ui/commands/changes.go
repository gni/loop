package commands

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loop/pkg/agent"
	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/process"
	"loop/pkg/storage"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func handleChangesCommand(ctx context.Context, a *agent.Agent, showDiff bool, w io.Writer, theme style.UITheme) {
	if ctx.Err() != nil {
		return
	}
	fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("Changes in this conversation"))
	count := 0
	inspectionFailed := false
	if a.Guided != nil {
		for _, snapshot := range a.Guided.Snapshots() {
			if ctx.Err() != nil {
				return
			}
			path, err := a.SafePath(snapshot.AbsPath)
			if err != nil || path != snapshot.AbsPath {
				fmt.Fprintf(w, "  Could not inspect %s: target is no longer accessible.\n", render.SanitizeTerminalText(snapshot.AbsPath))
				inspectionFailed = true
				continue
			}
			unlock, err := filetool.LockPathContext(ctx, path)
			if err != nil {
				return
			}
			current, err := storage.ReadFileContext(ctx, path, 8<<20)
			unlock()
			if ctx.Err() != nil {
				return
			}
			if err != nil && !os.IsNotExist(err) {
				fmt.Fprintf(w, "  Could not inspect %s: %s\n", render.SanitizeTerminalText(snapshot.AbsPath), render.SanitizeTerminalText(err.Error()))
				inspectionFailed = true
				continue
			}
			if (!snapshot.Existed && os.IsNotExist(err)) || (snapshot.Existed && err == nil && bytes.Equal(current, snapshot.Data)) {
				continue
			}
			status := "Modified"
			if !snapshot.Existed {
				status = "Created"
			}
			if os.IsNotExist(err) {
				status = "Deleted"
			}
			path = snapshot.AbsPath
			if rel, e := filepath.Rel(a.WorkspaceRoot, path); e == nil {
				path = rel
			}
			fmt.Fprintf(w, "  %s  %s\n", status, render.SanitizeTerminalText(path))
			count++
			if showDiff {
				if filetool.IsBinary(snapshot.Data) || filetool.IsBinary(current) {
					fmt.Fprintln(w, "  Binary file changed.")
					continue
				}
				diff, err := filetool.GenerateDisplayDiffContext(ctx, string(snapshot.Data), string(current), 3)
				if err != nil {
					return
				}
				if _, err := fmt.Fprint(w, render.SanitizeTerminalText(diff)+"\n"); err != nil {
					return
				}
			}
		}
	}
	if count == 0 && !inspectionFailed {
		fmt.Fprintln(w, "  No changes recorded by file tools.")
	}
	fmt.Fprintln(w, "\nGit working tree (includes shell edits and existing changes)")
	args := []string{"status", "--short", "--untracked-files=normal"}
	if showDiff {
		args = []string{"diff", "--no-ext-diff", "--no-textconv", "HEAD", "--"}
	}
	result, err := process.Run(ctx, process.Options{Executable: "git", Arguments: args, Directory: a.WorkspaceRoot, Timeout: 3 * time.Second, OutputLimit: 256 << 10})
	if err != nil && showDiff && strings.Contains(result.Stderr, "HEAD") {
		// Before the first commit, compare the index with the empty tree,
		// then include edits made after staging.
		result, err = process.Run(ctx, process.Options{Executable: "git", Arguments: []string{"diff", "--no-ext-diff", "--no-textconv", "--cached", "--"}, Directory: a.WorkspaceRoot, Timeout: 3 * time.Second, OutputLimit: 256 << 10})
		if err == nil {
			unstaged, unstagedErr := process.Run(ctx, process.Options{Executable: "git", Arguments: []string{"diff", "--no-ext-diff", "--no-textconv", "--"}, Directory: a.WorkspaceRoot, Timeout: 3 * time.Second, OutputLimit: 256 << 10})
			result.Stdout += unstaged.Stdout
			err = unstagedErr
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		fmt.Fprintln(w, "  Git view unavailable.")
		return
	}
	if result.Stdout == "" {
		fmt.Fprintln(w, "  No Git changes.")
		return
	}
	fmt.Fprintln(w, render.SanitizeTerminalText(result.Stdout))
	if !showDiff {
		fmt.Fprintln(w, "  /changes diff - show patches")
	}
}
