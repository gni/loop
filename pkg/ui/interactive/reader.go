package interactive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"loop/pkg/domain/limits"
	"loop/pkg/terminal"
	"loop/pkg/ui/input"
	"loop/pkg/ui/style"
)

type ReadResult struct {
	Data []byte
	Err  error
}

type readDeadliner interface {
	SetReadDeadline(time.Time) error
}

type SessionReader struct {
	context     context.Context
	cancelled   <-chan struct{}
	chanInput   <-chan byte
	inputChan   chan ReadResult
	readRequest chan struct{}
	readPending bool
	doneChan    chan struct{}
	readerDone  chan struct{}
	deadliner   readDeadliner
	closeMu     sync.Mutex
	closed      bool
	reading     bool
	closeOnce   sync.Once
	pending     []byte
	pendingErr  error
}

const (
	maxMenuInputBytes      = input.MaxPasteBytes
	maxTerminalEscapeBytes = 256
	maxTerminalStringBytes = 4096
)

type inputContextProvider interface{ GetInputContext() context.Context }
type inputChannelProvider interface{ GetInputChan() chan byte }
type inputReceiveChannelProvider interface{ GetInputChan() <-chan byte }

func IsNilReader(r io.Reader) bool {
	if r == nil {
		return true
	}
	v := reflect.ValueOf(r)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func NewSessionReader(rlInput io.Reader) *SessionReader {
	return newSessionReader(rlInput, 1024)
}

// Modal input must stop at the confirmation key instead of reading the next
// prompt's bytes. Channel-backed readers already preserve that boundary.
func modalSessionReader(in io.Reader) (*SessionReader, func()) {
	if sr, ok := in.(*SessionReader); ok && sr != nil {
		return sr, func() {}
	}
	sr := newSessionReader(in, 1)
	return sr, sr.Close
}

func newSessionReader(rlInput io.Reader, readSize int) *SessionReader {
	sr := &SessionReader{
		doneChan: make(chan struct{}),
	}

	if IsNilReader(rlInput) {
		sr.pendingErr = io.EOF
		return sr
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

	if d, ok := rlInput.(readDeadliner); ok {
		sr.deadliner = d
	}

	sr.inputChan = make(chan ReadResult, 1)
	sr.readRequest = make(chan struct{})
	sr.readerDone = make(chan struct{})
	done, cancelled := sr.doneChan, sr.cancelled
	go func() {
		defer close(sr.readerDone)
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

			sr.closeMu.Lock()
			if sr.closed {
				sr.closeMu.Unlock()
				return
			}
			sr.reading = true
			sr.closeMu.Unlock()

			n, err := rlInput.Read(readBuf)

			sr.closeMu.Lock()
			sr.reading = false
			sr.closeMu.Unlock()

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

// SetContext binds a cancellation context to the reader before reading begins.
func (sr *SessionReader) SetContext(ctx context.Context) {
	if sr == nil || ctx == nil {
		return
	}
	sr.context, sr.cancelled = ctx, ctx.Done()
}

func (sr *SessionReader) Close() {
	sr.closeOnce.Do(func() {
		sr.closeMu.Lock()
		sr.closed = true
		needInterrupt := sr.reading
		sr.closeMu.Unlock()

		if sr.doneChan != nil {
			close(sr.doneChan)
		}
		if needInterrupt && sr.deadliner != nil {
			if err := sr.deadliner.SetReadDeadline(time.Now()); err == nil {
				select {
				case <-sr.readerDone:
				case <-time.After(100 * time.Millisecond):
				}
				_ = sr.deadliner.SetReadDeadline(time.Time{})
			}
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

func (sr *SessionReader) readByteWithTimeout(d time.Duration) (byte, bool, error) {
	if len(sr.pending) > 0 {
		return sr.readByte(nil)
	}
	timeout := make(chan os.Signal, 1)
	timer := time.AfterFunc(d, func() {
		select {
		case timeout <- os.Interrupt:
		default:
		}
	})
	defer timer.Stop()
	return sr.readByte(timeout)
}

func (sr *SessionReader) consumeTerminalString(timeout time.Duration) error {
	prevEsc := false
	for range maxTerminalStringBytes {
		b, expired, err := sr.readByteWithTimeout(timeout)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if expired {
			return nil
		}
		if b == 0x07 {
			return nil
		}
		if prevEsc {
			if b == '\\' {
				return nil
			}
			prevEsc = (b == 0x1b)
			continue
		}
		if b == 0x1b {
			prevEsc = true
		}
	}
	return nil
}

func (sr *SessionReader) readCSI(key []byte, timeout time.Duration) ([]byte, bool, error) {
	for {
		if len(key) >= maxTerminalEscapeBytes {
			return nil, false, fmt.Errorf("terminal escape sequence exceeds %d bytes", maxTerminalEscapeBytes)
		}
		next, expired, err := sr.readByteWithTimeout(timeout)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, false, err
		}
		if err != nil || expired {
			return key, false, nil
		}
		if next < 0x20 || next >= 0x7f {
			sr.pending = append([]byte{next}, sr.pending...)
			return key, false, nil
		}
		key = append(key, next)
		if next >= 0x40 && next <= 0x7e {
			if len(key) == 3 && next == 'M' {
				// X10 mouse event: ESC [ M cb cx cy
				for range 3 {
					mb, mExpired, mErr := sr.readByteWithTimeout(timeout)
					if mErr != nil && !errors.Is(mErr, io.EOF) {
						return nil, false, mErr
					}
					if mErr != nil || mExpired {
						return key, false, nil
					}
					key = append(key, mb)
				}
				return key, false, nil
			}
			if string(key) == input.BracketedPasteStart {
				return sr.readPaste(key)
			}
			if norm, ok := input.NormalizeCSI(key); ok {
				return norm, false, nil
			}
			return key, false, nil
		}
	}
}

func (sr *SessionReader) readSS3(key []byte, timeout time.Duration) ([]byte, bool, error) {
	for {
		if len(key) >= maxTerminalEscapeBytes {
			return nil, false, fmt.Errorf("terminal escape sequence exceeds %d bytes", maxTerminalEscapeBytes)
		}
		next, expired, err := sr.readByteWithTimeout(timeout)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, false, err
		}
		if err != nil || expired {
			return key, false, nil
		}
		if next < 0x20 || next >= 0x7f {
			sr.pending = append([]byte{next}, sr.pending...)
			return key, false, nil
		}
		key = append(key, next)
		if next >= 0x40 && next <= 0x7e {
			if norm, ok := input.NormalizeSS3(key); ok {
				return norm, false, nil
			}
			return key, false, nil
		}
	}
}

func (sr *SessionReader) readUTF8Continuation(key []byte, runeStart int, timeout time.Duration) ([]byte, error) {
	for !utf8.FullRune(key[runeStart:]) && len(key)-runeStart < utf8.UTFMax {
		var (
			next    byte
			expired bool
			err     error
		)
		if timeout > 0 {
			next, expired, err = sr.readByteWithTimeout(timeout)
		} else {
			next, _, err = sr.readByte(nil)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, err
			}
			break
		}
		if expired {
			break
		}
		if next&0xc0 != 0x80 {
			sr.pending = append([]byte{next}, sr.pending...)
			break
		}
		key = append(key, next)
	}
	return key, nil
}

// ReadKeyOrResize frames one key, even when a terminal batches several keys
// in one read or splits an escape sequence across reads.
func (sr *SessionReader) ReadKeyOrResize(sigChan chan os.Signal) ([]byte, bool, error) {
	for {
		b, resized, err := sr.readByte(sigChan)
		if err != nil || resized {
			return nil, resized, err
		}
		key := []byte{b}
		if b == 27 {
			escTimeout := limits.EscapeDisambiguation()
			next, expired, err := sr.readByteWithTimeout(escTimeout)
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, false, err
			}
			if err != nil || expired {
				return key, false, nil
			}
			switch next {
			case ']', 'P', 'X', '^', '_':
				if err := sr.consumeTerminalString(escTimeout); err != nil {
					return nil, false, err
				}
				continue
			case '[':
				key = append(key, next)
				res, resized, err := sr.readCSI(key, escTimeout)
				if err != nil || resized || len(res) > 0 {
					return res, resized, err
				}
				continue
			case 'O':
				key = append(key, next)
				res, resized, err := sr.readSS3(key, escTimeout)
				if err != nil || resized || len(res) > 0 {
					return res, resized, err
				}
				continue
			default:
				key = append(key, next)
				if next >= utf8.RuneSelf {
					key, err = sr.readUTF8Continuation(key, 1, escTimeout)
					if err != nil {
						return nil, false, err
					}
				}
				return key, false, nil
			}
		}
		if b >= utf8.RuneSelf {
			key, err = sr.readUTF8Continuation(key, 0, 0)
			if err != nil {
				return nil, false, err
			}
		}
		return key, false, nil
	}
}

func (sr *SessionReader) readPaste(key []byte) ([]byte, bool, error) {
	overflow := false
	maxLen := input.MaxPasteBytes + len(input.BracketedPasteStart) + len(input.BracketedPasteEnd)
	var tail [len(input.BracketedPasteEnd)]byte
	tailLen := 0
	for {
		b, _, err := sr.readByte(nil)
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return nil, false, err
		}
		if len(key) < maxLen {
			key = append(key, b)
		} else {
			overflow = true
		}
		if tailLen < len(tail) {
			tail[tailLen] = b
			tailLen++
		} else {
			copy(tail[:], tail[1:])
			tail[len(tail)-1] = b
		}
		if tailLen == len(tail) && string(tail[:]) == input.BracketedPasteEnd {
			if overflow {
				return nil, false, fmt.Errorf("pasted input exceeds 1 MiB")
			}
			return key, false, nil
		}
	}
}

// Pasted controls are text, never navigation or confirmation. Keep newlines in
// the value, while removing controls and ANSI sequences that could alter terminal state.
func pastedKeyText(key []byte) (string, bool) {
	startBytes := []byte(input.BracketedPasteStart)
	endBytes := []byte(input.BracketedPasteEnd)
	if len(key) < len(startBytes)+len(endBytes) || !bytes.HasPrefix(key, startBytes) || !bytes.HasSuffix(key, endBytes) {
		return "", false
	}
	payload := key[len(startBytes) : len(key)-len(endBytes)]
	return input.NormalizePastedText(payload), true
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
	for len(chunk) > 0 {
		r, size := utf8.DecodeRune(chunk)
		raw := chunk[:size]
		chunk = chunk[size:]
		if r == utf8.RuneError && size == 1 {
			continue
		}
		switch {
		case r == '\r' || r == '\n':
			fmt.Fprint(out, "\r\n")
			return true, nil
		case r == 127 || r == 8:
			PopRuneFromBuilder(line, out)
		case r == 3 || r == 4:
			return false, fmt.Errorf("cancelled")
		case !unicode.IsControl(r):
			if size > maxMenuInputBytes-line.Len() {
				return false, fmt.Errorf("input exceeds 1 MiB")
			}
			line.Write(raw)
			_, _ = out.Write(raw)
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
	if err := writeMenuFrame(rlOutput, terminal.ShowCursor); err != nil {
		return "", err
	}
	defer fmt.Fprint(rlOutput, terminal.HideCursor)
	sr, closeReader := modalSessionReader(rlInput)
	defer closeReader()
	return sr.ReadLine(rlOutput)
}

func PopRuneFromBuilder(line *strings.Builder, out io.Writer) {
	if line.Len() == 0 {
		return
	}
	s := line.String()
	r, size := utf8.DecodeLastRuneInString(s)
	line.Reset()
	line.WriteString(s[:len(s)-size])
	w := max(1, style.RuneWidth(r))
	fmt.Fprint(out, strings.Repeat("\b", w)+"\x1b[K")
}
