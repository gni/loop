package interactive

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

type ReadResult struct {
	Data []byte
	Err  error
}

type readResult = ReadResult

type SessionReader struct {
	chanInput chan byte
	inputChan chan ReadResult
	doneChan  chan struct{}
}

type sessionReader = SessionReader

func NewSessionReader(rlInput io.Reader) *SessionReader {
	return newSessionReader(rlInput)
}

func newSessionReader(rlInput io.Reader) *sessionReader {
	sr := &sessionReader{
		doneChan: make(chan struct{}),
	}

	if cr, ok := rlInput.(interface{ GetInputChan() chan byte }); ok {
		sr.chanInput = cr.GetInputChan()
		return sr
	}

	sr.inputChan = make(chan readResult, 100)
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
				case sr.inputChan <- readResult{Data: data, Err: err}:
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

func (sr *SessionReader) Read(p []byte) (n int, err error) {
	data, _, err := sr.ReadKeyOrResize(nil)
	if err != nil {
		return 0, err
	}
	n = copy(p, data)
	return n, nil
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
			if b == 27 { // Escape character, wait for arrow keys
				for {
					select {
					case next, ok := <-sr.chanInput:
						if ok {
							buf = append(buf, next)
						} else {
							return buf, false, nil
						}
					case <-time.After(15 * time.Millisecond):
						return buf, false, nil
					}
				}
			}
			// Drain other buffered bytes
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
	if sr.chanInput != nil {
		for {
			select {
			case b, ok := <-sr.chanInput:
				if !ok {
					return "", fmt.Errorf("read error")
				}
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
					fmt.Fprint(rlOutput, string(b))
				}
			}
		}
	}

	for {
		res, ok := <-sr.inputChan
		if !ok || res.Err != nil {
			return "", fmt.Errorf("read error")
		}
		for _, b := range res.Data {
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
				fmt.Fprint(rlOutput, string(b))
			}
		}
	}
}

func ReadInputRaw(rlInput io.Reader, rlOutput io.Writer) (string, error) {
	return readInputRaw(rlInput, rlOutput)
}

func readInputRaw(rlInput io.Reader, rlOutput io.Writer) (string, error) {
	fmt.Fprint(rlOutput, "\x1b[?25h")
	defer fmt.Fprint(rlOutput, "\x1b[?25l")

	var sb strings.Builder
	var buf [1024]byte
	for {
		n, err := rlInput.Read(buf[:])
		if err != nil {
			return "", err
		}
		if n == 0 {
			continue
		}

		for i := 0; i < n; i++ {
			char := buf[i]

			if char == 13 || char == 10 {
				fmt.Fprint(rlOutput, "\r\n")
				return sb.String(), nil
			}

			if char == 127 || char == 8 {
				str := sb.String()
				if len(str) > 0 {
					sb.Reset()
					sb.WriteString(str[:len(str)-1])
					fmt.Fprint(rlOutput, "\b \b")
				}
				continue
			}

			if char == 3 || char == 4 {
				return "", fmt.Errorf("cancelled")
			}

			if char >= 32 && char <= 126 {
				sb.WriteByte(char)
				fmt.Fprint(rlOutput, string(char))
			}
		}
	}
}

func PopRuneFromBuilder(line *strings.Builder, out io.Writer) {
	popRuneFromBuilder(line, out)
}

func popRuneFromBuilder(line *strings.Builder, out io.Writer) {
	if line.Len() > 0 {
		s := line.String()
		runes := []rune(s)
		if len(runes) > 0 {
			truncated := string(runes[:len(runes)-1])
			line.Reset()
			line.WriteString(truncated)
			fmt.Fprint(out, "\b\x1b[K")
		}
	}
}
