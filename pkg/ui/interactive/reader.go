package interactive

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"loop/pkg/domain/limits"
)

type ReadResult struct {
	Data []byte
	Err  error
}

type SessionReader struct {
	context    context.Context
	cancelled  <-chan struct{}
	chanInput  chan byte
	inputChan  chan ReadResult
	doneChan   chan struct{}
	closeOnce  sync.Once
	pending    []byte
	pendingErr error
}

func NewSessionReader(rlInput io.Reader) *SessionReader {
	sr := &SessionReader{
		doneChan: make(chan struct{}),
	}

	if provider, ok := rlInput.(interface{ GetInputContext() context.Context }); ok {
		sr.context = provider.GetInputContext()
		if sr.context != nil {
			sr.cancelled = sr.context.Done()
		}
	}
	if cr, ok := rlInput.(interface{ GetInputChan() chan byte }); ok {
		sr.chanInput = cr.GetInputChan()
		return sr
	}

	sr.inputChan = make(chan ReadResult, 100)
	go func() {
		defer close(sr.inputChan)
		var readBuf [1024]byte
		for {
			n, err := rlInput.Read(readBuf[:])
			if n > 0 || err != nil {
				data := make([]byte, n)
				copy(data, readBuf[:n])
				select {
				case <-sr.doneChan:
					return
				case sr.inputChan <- ReadResult{Data: data, Err: err}:
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return sr
}

func (sr *SessionReader) Close() {
	sr.closeOnce.Do(func() {
		if sr.doneChan != nil {
			close(sr.doneChan)
		}
	})
}

// Read preserves bytes that do not fit in the caller's buffer.
func (sr *SessionReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	data, _, err := sr.ReadKeyOrResize(nil)
	if len(data) == 0 {
		return 0, err
	}
	n := copy(p, data)
	sr.pending = append(append([]byte(nil), data[n:]...), sr.pending...)
	return n, nil
}

func (sr *SessionReader) readByte(sigChan chan os.Signal) (byte, bool, error) {
	for {
		if sr.context != nil && sr.context.Err() != nil {
			return 0, false, sr.context.Err()
		}
		if len(sr.pending) > 0 {
			b := sr.pending[0]
			sr.pending = sr.pending[1:]
			return b, false, nil
		}
		if sr.pendingErr != nil {
			return 0, false, sr.pendingErr
		}
		if sr.chanInput != nil {
			select {
			case <-sr.cancelled:
				return 0, false, sr.context.Err()
			case <-sr.doneChan:
				return 0, false, io.EOF
			case <-sigChan:
				return 0, true, nil
			case b, ok := <-sr.chanInput:
				if !ok {
					return 0, false, io.EOF
				}
				return b, false, nil
			}
		}
		select {
		case <-sr.cancelled:
			return 0, false, sr.context.Err()
		case <-sr.doneChan:
			return 0, false, io.EOF
		case <-sigChan:
			return 0, true, nil
		case res, ok := <-sr.inputChan:
			if !ok {
				return 0, false, io.EOF
			}
			sr.pending, sr.pendingErr = res.Data, res.Err
		}
	}
}

// ReadKeyOrResize frames one key, even when a terminal batches several keys
// in one read or splits an escape sequence across reads.
func (sr *SessionReader) ReadKeyOrResize(sigChan chan os.Signal) ([]byte, bool, error) {
	b, resized, err := sr.readByte(sigChan)
	if err != nil || resized {
		return nil, resized, err
	}
	key := []byte{b}
	if b == 27 {
		timeout := make(chan os.Signal, 1)
		timer := time.AfterFunc(limits.EscapeDisambiguation(), func() { timeout <- os.Interrupt })
		defer timer.Stop()
		next, expired, err := sr.readByte(timeout)
		if err != nil || expired {
			return key, false, nil
		}
		if next != '[' && next != 'O' {
			sr.pending = append([]byte{next}, sr.pending...)
			return key, false, nil
		}
		key = append(key, next)
		for {
			next, expired, err = sr.readByte(timeout)
			if err != nil || expired {
				return key, false, nil
			}
			key = append(key, next)
			if next >= 0x40 && next <= 0x7e {
				return key, false, nil
			}
		}
	}
	if b >= utf8.RuneSelf {
		for !utf8.FullRune(key) && len(key) < utf8.UTFMax {
			next, _, err := sr.readByte(nil)
			if err != nil {
				break
			}
			key = append(key, next)
		}
	}
	return key, false, nil
}

func consumeLineBytes(chunk []byte, line *strings.Builder, out io.Writer) (bool, error) {
	for _, b := range chunk {
		switch {
		case b == '\r' || b == '\n':
			fmt.Fprint(out, "\r\n")
			return true, nil
		case b == 127 || b == 8:
			PopRuneFromBuilder(line, out)
		case b == 3 || b == 4:
			return false, fmt.Errorf("cancelled")
		case b >= 32:
			line.WriteByte(b)
			_, _ = out.Write([]byte{b})
		}
	}
	return false, nil
}

func (sr *SessionReader) ReadLine(rlOutput io.Writer) (string, error) {
	var line strings.Builder
	for {
		buf, _, err := sr.ReadKeyOrResize(nil)
		if err != nil {
			return "", fmt.Errorf("read error")
		}
		done, lineErr := consumeLineBytes(buf, &line, rlOutput)
		if lineErr != nil {
			return "", lineErr
		}
		if done {
			return line.String(), nil
		}
	}
}

func ReadInputRaw(rlInput io.Reader, rlOutput io.Writer) (string, error) {
	fmt.Fprint(rlOutput, "\x1b[?25h")
	defer fmt.Fprint(rlOutput, "\x1b[?25l")

	var sb strings.Builder
	var buf [1024]byte
	for {
		n, err := rlInput.Read(buf[:])
		if err != nil {
			return "", err
		}
		done, lineErr := consumeLineBytes(buf[:n], &sb, rlOutput)
		if lineErr != nil {
			return "", lineErr
		}
		if done {
			return sb.String(), nil
		}
	}
}

func PopRuneFromBuilder(line *strings.Builder, out io.Writer) {
	if line.Len() == 0 {
		return
	}
	s := line.String()
	_, size := utf8.DecodeLastRuneInString(s)
	line.Reset()
	line.WriteString(s[:len(s)-size])
	fmt.Fprint(out, "\b\x1b[K")
}
