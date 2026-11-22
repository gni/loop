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

type ctxReader struct {
	r   io.Reader
	ctx context.Context
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if c.ctx != nil {
		if err := c.ctx.Err(); err != nil {
			return 0, err
		}
	}
	n, err := c.r.Read(p)
	if c.ctx != nil {
		if cerr := c.ctx.Err(); cerr != nil {
			return 0, cerr
		}
	}
	return n, err
}

type Reader struct {
	scanner    *bufio.Scanner
	source     *ctxReader
	closer     io.Closer
	OnActivity func()
	remaining  int64
	limited    bool
}

// NewLimitedReader bounds one finite response, including comments and metadata.
// Long-lived event subscriptions should use NewReader instead.
func NewLimitedReader(input io.Reader, maxBytes int64) *Reader {
	r := NewReader(input)
	r.remaining, r.limited = maxBytes, true
	return r
}

func NewReader(input io.Reader) *Reader {
	source := &ctxReader{r: input}
	closer, _ := input.(io.Closer)
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 64<<10), MaxEventBytes)
	return &Reader{scanner: scanner, source: source, closer: closer}
}

func (r *Reader) Read(ctx context.Context) (Event, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return Event{}, err
		}
	}
	r.source.ctx = ctx
	defer func() { r.source.ctx = nil }()
	if ctx != nil && ctx.Done() != nil && r.closer != nil {
		closed := make(chan struct{})
		stop := context.AfterFunc(ctx, func() {
			defer close(closed)
			_ = r.closer.Close()
		})
		defer func() {
			if !stop() {
				<-closed
			}
		}()
	}

	var event Event
	var data strings.Builder
	seen := false
	for r.scanner.Scan() {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return Event{}, err
			}
		}
		if r.OnActivity != nil {
			r.OnActivity()
		}
		line := r.scanner.Text()
		if r.limited {
			r.remaining -= int64(len(line) + 1)
			if r.remaining < 0 {
				return Event{}, fmt.Errorf("SSE response exceeds byte limit")
			}
		}
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
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return Event{}, err
		}
	}
	if err := r.scanner.Err(); err != nil {
		return Event{}, err
	}
	if seen {
		event.Data = strings.TrimSuffix(data.String(), "\n")
		return event, nil
	}
	return Event{}, io.EOF
}
