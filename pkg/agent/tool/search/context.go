package search

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"loop/pkg/agent/tool/file"
	"loop/pkg/domain/limits"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/storage"
)

func (t *symbolTool) focusedContext(ctx domaintool.AgentContext, root string, files map[string]indexedFile, matches []symbolMatch) string {
	var output strings.Builder
	output.WriteString("\n\nFocused Go context (bounded source excerpts; use read before editing):\n")
	selected, names := 0, make(map[string]bool)
	importsShown := make(map[string]bool)
	for _, match := range matches {
		if err := ctx.Context().Err(); err != nil {
			break
		}
		if match.symbol.kind == "file" {
			continue
		}
		if selected == 3 {
			break
		}
		selected++
		name := match.symbol.name
		if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
			name = name[dot+1:]
		}
		names[name] = true
		path := filepath.Join(root, filepath.FromSlash(match.path))
		entry := files[path]
		fmt.Fprintf(&output, "\n%s:%d: %s %s\n", match.path, match.symbol.line, match.symbol.kind, match.symbol.name)
		if !importsShown[path] {
			importsShown[path] = true
			for i, reference := range entry.imports {
				if i == 8 {
					output.WriteString("[Additional imports omitted.]\n")
					break
				}
				if reference.alias == "" {
					fmt.Fprintf(&output, "import %q\n", reference.path)
				} else {
					fmt.Fprintf(&output, "import %s %q\n", reference.alias, reference.path)
				}
			}
		}
		source, err := currentIndexedSource(ctx, path, entry)
		if err != nil {
			fmt.Fprintf(&output, "[Source excerpt unavailable: %s. Read the current file.]\n", err)
			continue
		}
		start := max(1, match.symbol.line)
		end := min(len(source), min(max(start, match.symbol.endLine), start+39))
		for line := start; line <= end; line++ {
			fmt.Fprintf(&output, "%d: %s\n", line, file.UTF8SafePrefix(source[line-1], 200))
		}
		if match.symbol.endLine > end {
			output.WriteString("[Definition continues; request a targeted read.]\n")
		}
		if output.Len() >= 8192 {
			break
		}
	}
	if selected == 0 {
		output.WriteString("No Go declaration selected. Use a symbol name or read the matching file.\n")
	}
	type candidate struct {
		path string
		call callReference
	}
	var callers []candidate
	for path, entry := range files {
		if ctx.Context().Err() != nil {
			break
		}
		for _, call := range entry.calls {
			if names[call.name] {
				callers = append(callers, candidate{RelSlashPath(root, path), call})
			}
		}
	}
	sort.Slice(callers, func(i, j int) bool {
		if callers[i].path != callers[j].path {
			return callers[i].path < callers[j].path
		}
		return callers[i].call.line < callers[j].call.line
	})
	output.WriteString("\nCandidate callers in the search scope (syntax name matches; receiver types and dynamic dispatch are unresolved):\n")
	for _, caller := range callers[:min(12, len(callers))] {
		fmt.Fprintf(&output, "%s:%d: %s calls %s\n", caller.path, caller.call.line, caller.call.caller, caller.call.expression)
	}
	if len(callers) == 0 {
		output.WriteString("No indexed candidate calls found. This does not prove a symbol is unused.\n")
	}
	if len(callers) > 12 {
		fmt.Fprintf(&output, "[Showing 12 of %d candidate calls; narrow query or scope.]\n", len(callers))
	}
	budget := min(12<<10, limits.For(ctx).MaxOutputBytes)
	result := output.String()
	if len(result) > budget {
		result = file.UTF8SafePrefix(result, max(0, budget-96)) + "\n[Focused context truncated; narrow query or use targeted reads.]"
	}
	return result
}

// Read only bounded regular files and verify that cached locations still refer
// to the version being quoted. Index excerpts do not satisfy read-before-edit.
func currentIndexedSource(ctx domaintool.AgentContext, path string, indexed indexedFile) ([]string, error) {
	if err := ctx.Context().Err(); err != nil {
		return nil, err
	}
	safe, err := ctx.SafePath(path)
	if err != nil {
		return nil, err
	}
	if safe != path {
		return nil, fmt.Errorf("path changed")
	}
	source, err := storage.OpenRegularFile(path)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return nil, err
	}
	maxBytes := min(indexMaxFileBytes, limits.For(ctx).MaxReadFileBytes)
	if !info.Mode().IsRegular() || info.Size() > int64(maxBytes) {
		return nil, fmt.Errorf("file exceeds source limits or is not regular")
	}
	if info.Size() != indexed.size || !info.ModTime().Equal(indexed.modified) {
		return nil, fmt.Errorf("file changed since indexing")
	}
	data, err := io.ReadAll(io.LimitReader(source, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	after, err := source.Stat()
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes || int64(len(data)) != indexed.size || !after.ModTime().Equal(indexed.modified) {
		return nil, fmt.Errorf("file changed while reading")
	}
	return strings.Split(string(data), "\n"), nil
}
