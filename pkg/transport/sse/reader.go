package sse

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
)

const MaxEventBytes = 8 << 20

type Event struct {
	Name string
	Data string
	ID   string
}

type Reader struct {
	scanner    *bufio.Scanner
	OnActivity func()
}

func NewReader(input io.Reader) *Reader {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), MaxEventBytes)
	return &Reader{scanner: scanner}
}

func (r *Reader) Read(ctx context.Context) (Event, error) {
	var event Event
	var data strings.Builder
	seen := false
	for r.scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return Event{}, err
		}
		if r.OnActivity != nil {
			r.OnActivity()
		}
		line := r.scanner.Text()
		if line == "" {
			if seen {
				event.Data = strings.TrimSuffix(data.String(), "\n")
				return event, nil
			}
			event = Event{}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event.Name = value
		case "id":
			if !strings.ContainsRune(value, '\x00') {
				event.ID = value
			}
		case "data":
			if data.Len()+len(value)+1 > MaxEventBytes {
				return Event{}, fmt.Errorf("SSE event exceeds 8 MiB")
			}
			data.WriteString(value)
			data.WriteByte('\n')
			seen = true
		}
	}
	if err := r.scanner.Err(); err != nil {
		return Event{}, err
	}
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	if seen {
		event.Data = strings.TrimSuffix(data.String(), "\n")
		return event, nil
	}
	return Event{}, io.EOF
}
