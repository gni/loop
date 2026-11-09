package auth

import (
	"encoding/json"
	"fmt"
	"io"
)

// Read the complete bounded response so trailing data and oversized JSON cannot
// be accepted merely because a valid object appeared at the start of the body.
func decodeResponse(body io.Reader, destination any) error {
	const maxResponseBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("authentication response exceeds 1 MiB")
	}
	return json.Unmarshal(data, destination)
}
