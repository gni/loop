package http

import "fmt"

// StatusError retains the HTTP status independently of the human diagnostic.
// Consumers can classify local server failures without parsing error strings.
type StatusError struct {
	Code      int
	Body      string
	ReadError error
}

func (e *StatusError) Error() string {
	if e.ReadError != nil {
		return fmt.Sprintf("server returned non-200 status: %d. Body read failed: %v", e.Code, e.ReadError)
	}
	body := e.Body
	if body == "" {
		body = "(empty)"
	}
	return fmt.Sprintf("server returned non-200 status: %d. Body: %s", e.Code, body)
}
func (e *StatusError) Unwrap() error { return e.ReadError }
