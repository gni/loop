package interactive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"loop/pkg/domain/limits"
)

type ReadResult struct {
	Data []byte
	Err  error
}

type SessionReader struct {
	context     context.Context
	cancelled   <-chan struct{}
	chanInput   <-chan byte
	inputChan   chan ReadResult
	readRequest chan struct{}
	readPending bool
	doneChan    chan struct{}
	closeOnce   sync.Once
	pending     []byte
	pendingErr  error
}

const maxMenuInputBytes = 1 << 20
const maxTerminalEscapeBytes = 256
const pasteStart = "\x1b[200~"
const pasteEnd = "\x1b[201~"

type inputContextProvider interface{ GetInputContext() context.Context }
type inputChannelProvider interface{ GetInputChan() chan byte }
type inputReceiveChannelProvider interface{ GetInputChan() <-chan byte }

func NewSessionReader(rlInput io.Reader) *SessionReader {
	return newSessionReader(rlInput, 1024)
}

// Modal input must stop at the confirmation key instead of reading the next
// prompt's bytes. Channel-backed readers already preserve that boundary.
func modalSessionReader(input io.Reader) (*SessionReader, func()) {
	if sr, ok := input.(*SessionReader); ok {
		return sr, func() {}
	}
	sr := newSessionReader(input, 1)
	return sr, sr.Close
}

func newSessionReader(rlInput io.Reader, readSize int) *SessionReader {
	sr := &SessionReader{
		doneChan: make(chan struct{}),
	}

	if provider, ok := rlInput.(inputContextProvider); ok {
		sr.context = provider.GetInputContext()
		if sr.context != nil {
			sr.cancelled = sr.context.Done()
		}
	}
	if cr, ok := rlInput.(inputChannelProvider); ok {
		sr.chanInput = cr.GetInputChan()
	}
	if cr, ok := rlInput.(inputReceiveChannelProvider); ok {
		sr.chanInput = cr.GetInputChan()
	}
	if sr.chanInput != nil {
		return sr
	}
	if rlInput == nil {
		sr.pendingErr = io.EOF
		return sr
	}

	sr.inputChan = make(chan ReadResult, 1)
	sr.readRequest = make(chan struct{})
	done, cancelled := sr.doneChan, sr.cancelled
	go func() {
		defer close(sr.inputChan)
		readBuf := make([]byte, readSize)
		emptyReads := 0
		for {
			select {
			case <-done:
				return
			case <-cancelled:
				return
			case <-sr.readRequest:
			}
			n, err := rlInput.Read(readBuf)
			if n == 0 && err == nil {
				emptyReads++
				if emptyReads >= 100 {
					err = io.ErrNoProgress
				}
			} else {
				emptyReads = 0
			}
			data := append([]byte(nil), readBuf[:n]...)
			select {
			case <-done:
				return
			case <-cancelled:
				return
			case sr.inputChan <- ReadResult{Data: data, Err: err}:
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

// Read returns raw bytes; key framing belongs to ReadKeyOrResize.
func (sr *SessionReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n := 0
	for n < len(p) {
		b, _, err := sr.readByte(nil)
		if err != nil {
			return n, err
		}
		p[n] = b
		n++
		if len(sr.pending) == 0 && len(sr.chanInput) == 0 {
			break
		}
	}
	return n, nil
}

func (sr *SessionReader) readByte(sigChan chan os.Signal) (byte, bool, error) {
	for {
		select {
		case <-sr.doneChan:
			return 0, false, io.EOF
		default:
		}
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
			// Bytes already buffered belong to this key, even when a timer or
			// resize notification became ready while the caller was painting.
			select {
			case b, ok := <-sr.chanInput:
				if !ok {
					return 0, false, io.EOF
				}
				return b, false, nil
			default:
			}
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
		if sr.inputChan == nil {
			return 0, false, io.EOF
		}
		select {
		case res, ok := <-sr.inputChan:
			sr.readPending = false
			if !ok {
				return 0, false, io.EOF
			}
			sr.pending, sr.pendingErr = res.Data, res.Err
			continue
		default:
		}
		if !sr.readPending {
			select {
			case <-sigChan:
				return 0, true, nil
			default:
			}
			select {
			case <-sr.cancelled:
				return 0, false, sr.context.Err()
			case <-sr.doneChan:
				return 0, false, io.EOF
			case <-sigChan:
				return 0, true, nil
			case sr.readRequest <- struct{}{}:
				sr.readPending = true
			case res, ok := <-sr.inputChan:
				if !ok {
					return 0, false, io.EOF
				}
				sr.pending, sr.pendingErr = res.Data, res.Err
				continue
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
			sr.readPending = false
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
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, false, err
		}
		if err != nil || expired {
			return key, false, nil
		}
		if next != '[' && next != 'O' {
			sr.pending = append([]byte{next}, sr.pending...)
			return key, false, nil
		}
		key = append(key, next)
		for {
			if len(key) >= maxTerminalEscapeBytes {
				return nil, false, fmt.Errorf("terminal escape sequence exceeds %d bytes", maxTerminalEscapeBytes)
			}
			next, expired, err = sr.readByte(timeout)
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, false, err
			}
			if err != nil || expired {
				return key, false, nil
			}
			key = append(key, next)
			if next >= 0x40 && next <= 0x7e {
				if string(key) == pasteStart {
					return sr.readPaste(key)
				}
				return key, false, nil
			}
		}
	}
	if b >= utf8.RuneSelf {
		for !utf8.FullRune(key) && len(key) < utf8.UTFMax {
			next, _, err := sr.readByte(nil)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					return nil, false, err
				}
				break
			}
			if next&0xc0 != 0x80 {
				sr.pending = append([]byte{next}, sr.pending...)
				break
			}
			key = append(key, next)
		}
	}
	return key, false, nil
}

func (sr *SessionReader) readPaste(key []byte) ([]byte, bool, error) {
	overflow := false
	tail := ""
	for {
		b, _, err := sr.readByte(nil)
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return nil, false, err
		}
		if len(key) < maxMenuInputBytes+len(pasteStart)+len(pasteEnd) {
			key = append(key, b)
		} else {
			overflow = true
		}
		tail += string([]byte{b})
		if len(tail) > len(pasteEnd) {
			tail = tail[len(tail)-len(pasteEnd):]
		}
		if tail == pasteEnd {
			if overflow {
				return nil, false, fmt.Errorf("pasted input exceeds 1 MiB")
			}
			return key, false, nil
		}
	}
}

// Pasted controls are text, never navigation or confirmation. Keep newlines in
// the value, while removing controls that could alter terminal state on echo.
func pastedKeyText(key []byte) (string, bool) {
	text := string(key)
	if !strings.HasPrefix(text, pasteStart) || !strings.HasSuffix(text, pasteEnd) {
		return "", false
	}
	text = strings.TrimSuffix(strings.TrimPrefix(text, pasteStart), pasteEnd)
	text = strings.ToValidUTF8(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text), true
}

func appendPastedInput(key []byte, line *strings.Builder, output io.Writer) (bool, error) {
	text, pasted := pastedKeyText(key)
	if !pasted {
		return false, nil
	}
	if len(text) > maxMenuInputBytes-line.Len() {
		return true, fmt.Errorf("input exceeds 1 MiB")
	}
	line.WriteString(text)
	_, err := fmt.Fprint(output, strings.NewReplacer("\n", " ", "\t", " ").Replace(text))
	return true, err
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
			if line.Len() >= maxMenuInputBytes {
				return false, fmt.Errorf("input exceeds 1 MiB")
			}
			line.WriteByte(b)
			_, _ = out.Write([]byte{b})
		}
	}
	return false, nil
}

func (sr *SessionReader) ReadLine(rlOutput io.Writer) (string, error) {
	checkedOutput := &modalOutput{Writer: rlOutput}
	rlOutput = checkedOutput
	var line strings.Builder
	for {
		buf, _, err := sr.ReadKeyOrResize(nil)
		if err != nil {
			return "", fmt.Errorf("read error: %w", err)
		}
		if pasted, err := appendPastedInput(buf, &line, rlOutput); pasted {
			if err != nil {
				return "", err
			}
			continue
		}
		if string(buf) == "\x1b" {
			return "", ErrSelectionCancelled
		}
		// Navigation and terminal reports are not editable text.
		if len(buf) > 0 && buf[0] == 27 {
			continue
		}
		if !utf8.Valid(buf) {
			continue
		}
		done, lineErr := consumeLineBytes(buf, &line, rlOutput)
		if lineErr != nil {
			return "", lineErr
		}
		if checkedOutput.err != nil {
			return "", checkedOutput.err
		}
		if done {
			return line.String(), nil
		}
	}
}

func ReadInputRaw(rlInput io.Reader, rlOutput io.Writer) (string, error) {
	if err := writeMenuFrame(rlOutput, "\x1b[?25h"); err != nil {
		return "", err
	}
	defer fmt.Fprint(rlOutput, "\x1b[?25l")
	sr, closeReader := modalSessionReader(rlInput)
	defer closeReader()
	return sr.ReadLine(rlOutput)
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
