package search

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"loop/pkg/agent/tool/file"
	"loop/pkg/domain/limits"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/storage"
)

const (
	indexMaxFiles     = 10000
	indexMaxSymbols   = 30000
	indexMaxCalls     = 30000
	indexMaxFileBytes = 256 << 10
	indexMaxReadBytes = 16 << 20
)

type codeSymbol struct {
	name, kind string
	line       int
	endLine    int
}

type indexedFile struct {
	size     int64
	modified time.Time
	symbols  []codeSymbol
	imports  []importReference
	calls    []callReference
	partial  bool
}

type symbolMatch struct {
	path   string
	symbol codeSymbol
	score  int
}

type symbolTool struct {
	mu    sync.Mutex
	root  string
	files map[string]indexedFile
}

func NewSymbolTool() domaintool.ToolExecutor { return &symbolTool{} }
func (t *symbolTool) Name() string           { return "search_symbols" }
func (t *symbolTool) PromptSnippet() string {
	return domaintool.FormatToolSnippet(t.Name(), "Locate Go declarations and repository files")
}
func (t *symbolTool) PromptGuidelines() []string {
	return []string{"Use search_symbols to locate Go definitions. Set include_context=true for a bounded pack of source excerpts, imports, and candidate callers. Candidate callers match syntax names without resolving receiver types; they are not a complete reference graph. Read current target files before editing."}
}
func (t *symbolTool) Definition() domaintool.Tool {
	return domaintool.NewFunctionTool(t.Name(), "Search a bounded local index of Go functions, methods, types, variables, constants, and repository file paths. No server or model request. Reconciles changed and deleted files on each search. Honors search ignores and skips symlinks. Read returned locations before editing; this does not observe whole file contents.", map[string]domaintool.SchemaProp{
		"query":           domaintool.StringProp("Name or path fragment; space-separated terms must all match."),
		"path":            domaintool.StringProp("Optional file or directory scope within the workspace (default: .)."),
		"limit":           domaintool.NumberProp("Maximum results, 1-100 (default: 30)."),
		"include_context": domaintool.BoolProp("Include source excerpts and imports for up to 3 definitions, plus up to 12 candidate call sites in the search scope. Name matches are not resolved references."),
	}, "query")
}

// Invalidate drops cached declarations even when a write preserves size and mtime.
func (t *symbolTool) Invalidate(path string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.files, path)
}

func (t *symbolTool) Execute(ctx domaintool.AgentContext, arguments string) (string, error) {
	var args struct {
		Query          string `json:"query"`
		Path           string `json:"path"`
		Limit          int    `json:"limit"`
		IncludeContext bool   `json:"include_context"`
	}
	if err := domaintool.ParseToolArguments(arguments, &args, func(s string) { args.Query = s }); err != nil {
		return "", err
	}
	query := strings.ToLower(strings.TrimSpace(args.Query))
	if query == "" || len(query) > 256 {
		return "", fmt.Errorf("query must contain 1-256 bytes")
	}
	scope, err := ctx.SafePath(domaintool.FirstNonEmpty(args.Path, "."))
	if err != nil {
		return "", err
	}
	root := ctx.GetWorkspaceRoot()
	if err := validateSearchPath(scope, root); err != nil {
		return "", err
	}
	if _, err := os.Stat(scope); err != nil {
		return "", err
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 30
	}
	limit = min(limit, 100)

	t.mu.Lock()
	defer t.mu.Unlock()
	if err := ctx.Context().Err(); err != nil {
		return "", err
	}
	if t.root != root {
		t.root, t.files = root, nil
	}
	patterns := LoadGitIgnore(root)
	next := make(map[string]indexedFile)
	readBytes, symbolCount, callCount, visited := int64(0), 0, 0, 0
	partial := false
	err = filepath.WalkDir(scope, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Context().Err(); err != nil {
			return err
		}
		visited++
		if visited > indexMaxFiles*4 || len(next) >= indexMaxFiles {
			partial = true
			return fs.SkipAll
		}
		if walkErr != nil {
			partial = true
			return nil
		}
		if skip, skipErr := ShouldSkipWalkEntry(path, d, root, patterns); skip {
			return skipErr
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			partial = true
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		entry, cached := t.files[path]
		if !cached || entry.partial || entry.size != info.Size() || !entry.modified.Equal(info.ModTime()) {
			entry = indexedFile{size: info.Size(), modified: info.ModTime()}
			if strings.EqualFold(filepath.Ext(path), ".go") {
				maxBytes := min(indexMaxFileBytes, limits.For(ctx).MaxReadFileBytes)
				if info.Size() > int64(maxBytes) || readBytes+info.Size() > indexMaxReadBytes {
					entry.partial = true
				} else if safe, safeErr := ctx.SafePath(path); safeErr != nil || safe != path {
					entry.partial = true
				} else if source, openErr := storage.OpenRegularFile(path); openErr != nil {
					entry.partial = true
				} else {
					data, readErr := io.ReadAll(io.LimitReader(source, int64(maxBytes)+1))
					_ = source.Close()
					readBytes += int64(len(data))
					if readErr != nil || len(data) > maxBytes {
						entry.partial = true
					} else {
						metadata := parseGoMetadata(path, data)
						entry.symbols, entry.imports, entry.calls, entry.partial = metadata.symbols, metadata.imports, metadata.calls, metadata.partial
					}
				}
			}
		}
		if len(entry.symbols) > indexMaxSymbols-symbolCount {
			entry.symbols = entry.symbols[:max(0, indexMaxSymbols-symbolCount)]
			entry.partial = true
		}
		if len(entry.calls) > indexMaxCalls-callCount {
			entry.calls = entry.calls[:max(0, indexMaxCalls-callCount)]
			entry.partial = true
		}
		callCount += len(entry.calls)
		symbolCount += len(entry.symbols)
		partial = partial || entry.partial
		next[path] = entry
		return nil
	})
	if err != nil {
		return "", err
	}
	// Commit only completed scans. Missing paths disappear from the cache.
	t.files = next
	var matches []symbolMatch
	terms := strings.Fields(query)
	for path, entry := range next {
		rel := RelSlashPath(root, path)
		candidates := append([]codeSymbol{{name: filepath.Base(path), kind: "file", line: 1}}, entry.symbols...)
		for _, symbol := range candidates {
			name := strings.ToLower(symbol.name)
			haystack := name + " " + strings.ToLower(rel)
			found := true
			for _, term := range terms {
				if !strings.Contains(haystack, term) {
					found = false
					break
				}
			}
			if !found {
				continue
			}
			score := 3
			shortName := name[strings.LastIndexByte(name, '.')+1:]
			if name == query || shortName == query {
				score = 0
			} else if strings.HasPrefix(name, query) {
				score = 1
			} else if strings.Contains(name, query) {
				score = 2
			}
			matches = append(matches, symbolMatch{rel, symbol, score})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.score != b.score {
			return a.score < b.score
		}
		if a.path != b.path {
			return a.path < b.path
		}
		if a.symbol.line != b.symbol.line {
			return a.symbol.line < b.symbol.line
		}
		return a.symbol.name < b.symbol.name
	})
	lines := make([]string, 0, min(limit, len(matches)))
	for _, m := range matches[:min(limit, len(matches))] {
		lines = append(lines, fmt.Sprintf("%s:%d: %s %s", m.path, m.symbol.line, m.symbol.kind, m.symbol.name))
	}
	result := file.TruncateHead(lines, limit, limits.For(ctx).MaxOutputBytes)
	output := result.Content
	if len(matches) == 0 {
		output = "No matching indexed definitions or paths. Broaden the query or use grep for references and non-Go definitions."
	}
	if len(matches) > result.ShownLines {
		output += fmt.Sprintf("\nShowing %d of %d matches; narrow query or path for the rest.", result.ShownLines, len(matches))
	}
	if args.IncludeContext && len(matches) > 0 {
		output += t.focusedContext(ctx, root, next, matches)
		if len(output) > limits.For(ctx).MaxOutputBytes {
			output = file.UTF8SafePrefix(output, max(0, limits.For(ctx).MaxOutputBytes-128)) + "\n[Context pack truncated to the output byte limit; narrow query/path or use read.]"
		}
	}
	if partial {
		output += "\nIndex is partial: unreadable, oversized, invalid, or capped files may omit definitions. Narrow path or use read/grep."
	}
	if err := ctx.Context().Err(); err != nil {
		return "", err
	}
	return output, nil
}
