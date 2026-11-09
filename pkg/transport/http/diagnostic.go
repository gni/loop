package http

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

var diagnosticURL = regexp.MustCompile(`https?://[^\s"'<>]+`)
var diagnosticBearer = regexp.MustCompile(`(?i)\bbearer[ \t]+[a-z0-9._~+/-]+=*`)
var diagnosticCredential = regexp.MustCompile(`(?i)((?:api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret)["']?\s*[:=]\s*["']?)[^\s"',;&}]+`)

// RedactURL keeps routing context without printing embedded credentials or query tokens.
func RedactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "[invalid URL]"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}

// RedactDiagnostic removes terminal controls and common credential fields.
func RedactDiagnostic(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text)
	text = diagnosticURL.ReplaceAllStringFunc(text, RedactURL)
	text = diagnosticBearer.ReplaceAllString(text, "Bearer [redacted]")
	return diagnosticCredential.ReplaceAllString(text, "${1}[redacted]")
}

type diagnosticError struct{ cause error }

func (e *diagnosticError) Error() string { return RedactDiagnostic(e.cause.Error()) }
func (e *diagnosticError) Unwrap() error { return e.cause }

// RedactError preserves error classification while sanitizing its printed diagnostic.
func RedactError(err error) error {
	if err == nil {
		return nil
	}
	return &diagnosticError{cause: err}
}
