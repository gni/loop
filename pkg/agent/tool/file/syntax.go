package file

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/scanner"
	"go/token"
	"path/filepath"
	"strings"

	"loop/pkg/domain/evidence"
)

// ValidateSyntax inspects modified file content for syntax errors using standard
// library parsers. It returns an actionable warning string if errors are detected,
// or an empty string if syntax is valid or the file type is unhandled.
func ValidateSyntax(filename string, data []byte) string {
	_, warning := inspectSyntax(filename, data)
	return warning
}

func inspectSyntax(filename string, data []byte) ([]evidence.Diagnostic, string) {
	if len(data) == 0 {
		return nil, ""
	}

	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".go":
		fset := token.NewFileSet()
		// Parse with SkipObjectResolution for speed; we only validate syntax grammar.
		if _, err := parser.ParseFile(fset, filepath.Base(filename), data, parser.SkipObjectResolution); err != nil {
			var diagnostics []evidence.Diagnostic
			var parseErrors scanner.ErrorList
			if errors.As(err, &parseErrors) {
				for _, entry := range parseErrors {
					diagnostics = append(diagnostics, evidence.Diagnostic{Severity: "error", Message: entry.Msg, Line: entry.Pos.Line, Column: entry.Pos.Column})
					if len(diagnostics) == 8 {
						break
					}
				}
			} else {
				diagnostics = append(diagnostics, evidence.Diagnostic{Severity: "error", Message: err.Error()})
			}
			return diagnostics, fmt.Sprintf("\n\n[Syntax Warning: Go syntax error detected in %s:\n%v\nPlease review and correct syntax before proceeding.]", filepath.Base(filename), err)
		}
	case ".json":
		if !json.Valid(data) {
			var value json.RawMessage
			parseErr := json.Unmarshal(data, &value)
			d := evidence.Diagnostic{Severity: "error", Message: "Invalid JSON syntax"}
			var syntaxErr *json.SyntaxError
			if errors.As(parseErr, &syntaxErr) {
				offset := min(max(0, int(syntaxErr.Offset)-1), len(data))
				d.Message = syntaxErr.Error()
				d.Line = bytes.Count(data[:offset], []byte{'\n'}) + 1
				d.Column = offset - bytes.LastIndexByte(data[:offset], '\n')
			}
			return []evidence.Diagnostic{d}, fmt.Sprintf("\n\n[Syntax Warning: Invalid JSON syntax detected in %s. Please ensure valid JSON formatting.]", filepath.Base(filename))
		}
	}

	return nil, ""
}
