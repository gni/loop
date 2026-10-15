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
				for {
					select {
					case next, ok := <-sr.chanInput:
						if ok {
							buf = append(buf, next)
						} else {
							return buf, false, nil
						}
					case <-time.After(limits.EscapeDisambiguation()):
						return buf, false, nil
					}
				}
			}
			for {
				select {
				case next, ok := <-sr.chanInput:
					if ok {
						buf = append(buf, next)
					} else {
						return buf, false, nil
					}
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

func (sr *SessionReader) ReadLine(rlOutput io.Writer) (string, error) {
	var line strings.Builder
	for {
		buf, _, err := sr.ReadKeyOrResize(nil)
		if err != nil {
			return "", fmt.Errorf("read error")
		}
		for _, b := range buf {
			if b == '\r' || b == '\n' {
				fmt.Fprint(rlOutput, "\r\n")
				return line.String(), nil
			}
			if b == 127 || b == 8 {
				PopRuneFromBuilder(&line, rlOutput)
				continue
			}
			if b == 3 || b == 4 {
				return "", fmt.Errorf("cancelled")
			}
			if b >= 32 {
				line.WriteByte(b)
				_, _ = rlOutput.Write([]byte{b})
			}
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
		for i := 0; i < n; i++ {
			char := buf[i]
			if char == 13 || char == 10 {
				fmt.Fprint(rlOutput, "\r\n")
				return sb.String(), nil
			}
			if char == 127 || char == 8 {
				PopRuneFromBuilder(&sb, rlOutput)
				continue
			}
			if char == 3 || char == 4 {
				return "", fmt.Errorf("cancelled")
			}
			if char >= 32 {
				sb.WriteByte(char)
				_, _ = rlOutput.Write([]byte{char})
			}
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