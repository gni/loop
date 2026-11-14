package search

import (
	"crypto/sha256"
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
	digest      [32]byte
	changeStamp int64
	size        int64
	modified    time.Time
	symbols     []codeSymbol
	imports     []importReference
	calls       []callReference
	partial     bool
	retry       bool // Source was unread or metadata was clipped by a scan-wide limit.
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
	return domaintool.FormatToolSnippet(t.Name(), "Locate Go, JS/TS and Python declarations and repository files")
}
func (t *symbolTool) PromptGuidelines() []string {
	return []string{"Use search_symbols for Go, JS/TS and Python declarations. include_context returns bounded excerpts; Go also includes imports and candidate callers. Script declarations are lexical locations. Read current files before editing."}
}
func (t *symbolTool) Definition() domaintool.Tool {
	return domaintool.NewFunctionTool(t.Name(), "Search bounded Go AST declarations, lexical JS/TS and Python declarations, and file paths. No model/server. Refreshes changed sources, honors ignores, skips symlinks. Read locations before editing; index excerpts do not establish whole-file observation.", map[string]domaintool.SchemaProp{
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
		stamp, stampKnown := storage.FileChangeStamp(info)
		needsCheck := !cached || entry.retry || entry.size != info.Size() || !entry.modified.Equal(info.ModTime()) || !stampKnown || entry.changeStamp != stamp
		if parser := parserFor(path); parser != nil && needsCheck {
			maxBytes := min(indexMaxFileBytes, limits.For(ctx).MaxReadFileBytes)
			if info.Size() > int64(maxBytes) || readBytes+info.Size() > indexMaxReadBytes {
				entry = indexedFile{size: info.Size(), modified: info.ModTime()}
				entry.partial, entry.retry = true, true
			} else if safe, safeErr := ctx.SafePath(path); safeErr != nil || safe != path {
				entry = indexedFile{size: info.Size(), modified: info.ModTime(), partial: true, retry: true}
			} else if source, openErr := storage.OpenRegularFile(path); openErr != nil {
				entry = indexedFile{size: info.Size(), modified: info.ModTime(), partial: true, retry: true}
			} else {
				data, readErr := io.ReadAll(io.LimitReader(source, int64(maxBytes)+1))
				_ = source.Close()
				readBytes += int64(len(data))
				if readErr != nil || len(data) > maxBytes {
					entry = indexedFile{size: info.Size(), modified: info.ModTime(), partial: true, retry: true}
				} else {
					digest := sha256.Sum256(data)
					if !cached || entry.retry || entry.digest != digest {
						metadata := parser.Parse(path, data)
						entry = indexedFile{size: info.Size(), modified: info.ModTime(), changeStamp: stamp, digest: digest, symbols: metadata.symbols, imports: metadata.imports, calls: metadata.calls, partial: metadata.partial}
					} else {
						entry.size, entry.modified = info.Size(), info.ModTime()
						entry.changeStamp = stamp
					}
				}
			}
		} else if parserFor(path) == nil {
			entry = indexedFile{size: info.Size(), modified: info.ModTime()}
		}
		if len(entry.symbols) > indexMaxSymbols-symbolCount {
			entry.symbols = entry.symbols[:max(0, indexMaxSymbols-symbolCount)]
			entry.partial, entry.retry = true, true
		}
		if len(entry.calls) > indexMaxCalls-callCount {
			entry.calls = entry.calls[:max(0, indexMaxCalls-callCount)]
			entry.partial, entry.retry = true, true
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
	t.commitIndex(scope, next)
	var matches []symbolMatch
	terms := strings.Fields(query)
	for path, entry := range next {
		rel := RelSlashPath(root, path)
		lowerRel := strings.ToLower(rel)
		candidates := append([]codeSymbol{{name: filepath.Base(path), kind: "file", line: 1}}, entry.symbols...)
		for _, symbol := range candidates {
			name := strings.ToLower(symbol.name)
			found := true
			for _, term := range terms {
				if !strings.Contains(name, term) && !strings.Contains(lowerRel, term) {
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
		output = "No matching indexed definitions or paths. Broaden the query or use grep for references and other languages."
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

// Keep entries outside a completed narrow scan, without exposing them in its
// results or exceeding the index limits. Rescanned scopes replace old entries,
// including paths that have been removed or are now ignored.
func (t *symbolTool) commitIndex(scope string, next map[string]indexedFile) {
	var sourceBytes int64
	for _, entry := range next {
		sourceBytes += indexedSourceBytes(entry)
	}
	if (scope == t.root || len(t.files) == 0) && sourceBytes <= indexMaxReadBytes {
		t.files = next
		return
	}
	paths := make([]string, 0, len(next))
	for path := range next {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	cache := make(map[string]indexedFile, min(indexMaxFiles, len(next)+len(t.files)))
	symbolCount, callCount := 0, 0
	sourceBytes = 0
	retain := func(path string, entry indexedFile) {
		bytes := indexedSourceBytes(entry)
		if len(cache) >= indexMaxFiles || symbolCount+len(entry.symbols) > indexMaxSymbols || callCount+len(entry.calls) > indexMaxCalls || sourceBytes+bytes > indexMaxReadBytes {
			return
		}
		cache[path] = entry
		symbolCount += len(entry.symbols)
		callCount += len(entry.calls)
		sourceBytes += bytes
	}
	for _, path := range paths {
		retain(path, next[path])
	}
	paths = paths[:0]
	for path := range t.files {
		if path != scope && !strings.HasPrefix(path, scope+string(filepath.Separator)) {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		retain(path, t.files[path])
	}
	t.files = cache
}

// Script declaration names can retain their entire masked source string. Bound
// the source represented by cached metadata as well as the declaration counts.
func indexedSourceBytes(entry indexedFile) int64 {
	if cap(entry.symbols)+cap(entry.imports)+cap(entry.calls) == 0 {
		return 0
	}
	return entry.size
}
