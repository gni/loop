package interactive

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"loop/pkg/domain/limits"
)

type ReadResult struct {
	Data []byte
	Err  error
}

type SessionReader struct {
	chanInput chan byte
	inputChan chan ReadResult
	doneChan  chan struct{}
}

func NewSessionReader(rlInput io.Reader) *SessionReader {
	sr := &SessionReader{
		doneChan: make(chan struct{}),
	}

	if cr, ok := rlInput.(interface{ GetInputChan() chan byte }); ok {
		sr.chanInput = cr.GetInputChan()
		return sr
	}

	sr.inputChan = make(chan ReadResult, 100)
	go func() {
		var readBuf [1024]byte
		for {
			n, err := rlInput.Read(readBuf[:])
			if n > 0 {
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
	if sr.doneChan != nil {
		select {
		case <-sr.doneChan:
		default:
			close(sr.doneChan)
		}
	}
}

func (sr *SessionReader) Read(p []byte) (int, error) {
	data, _, err := sr.ReadKeyOrResize(nil)
	if err != nil {
		return 0, err
	}
	return copy(p, data), nil
}

func (sr *SessionReader) ReadKeyOrResize(sigChan chan os.Signal) ([]byte, bool, error) {
	if sr.chanInput != nil {
		select {
		case <-sigChan:
			return nil, true, nil
		case b, ok := <-sr.chanInput:
			if !ok {
				return nil, false, io.EOF
			}
			buf := []byte{b}
			if b == 27 {
				timeout := limits.EscapeDisambiguation()
				timer := time.NewTimer(timeout)
				defer timer.Stop()
				for {
					select {
					case next, ok := <-sr.chanInput:
						if !ok {
							return buf, false, nil
						}
						buf = append(buf, next)
						if !timer.Stop() {
							select {
							case <-timer.C:
							default:
							}
						}
						timer.Reset(timeout)
					case <-timer.C:
						return buf, false, nil
					}
				}
			}
			for {
				select {
				case next, ok := <-sr.chanInput:
					if !ok {
						return buf, false, nil
					}
					buf = append(buf, next)
				default:
					return buf, false, nil
				}
			}
		}
	}

	select {
	case <-sigChan:
		return nil, true, nil
	case res, ok := <-sr.inputChan:
		if !ok {
			return nil, false, io.EOF
		}
		return res.Data, false, res.Err
	}
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