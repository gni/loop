package file

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
)

// ValidateSyntax inspects modified file content for syntax errors using standard
// library parsers. It returns an actionable warning string if errors are detected,
// or an empty string if syntax is valid or the file type is unhandled.
func ValidateSyntax(filename string, data []byte) string {
	if len(data) == 0 {
		return ""
	}

	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".go":
		fset := token.NewFileSet()
		// Parse with SkipObjectResolution for speed; we only validate syntax grammar.
		if _, err := parser.ParseFile(fset, filepath.Base(filename), data, parser.SkipObjectResolution); err != nil {
			return fmt.Sprintf("\n\n[Syntax Warning: Go syntax error detected in %s:\n%v\nPlease review and correct syntax before proceeding.]", filepath.Base(filename), err)
		}
	case ".json":
		if !json.Valid(data) {
			return fmt.Sprintf("\n\n[Syntax Warning: Invalid JSON syntax detected in %s. Please ensure valid JSON formatting.]", filepath.Base(filename))
		}
	}

	return ""
}
