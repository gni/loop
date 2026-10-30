package output

import (
	"bytes"
	"io"
)

// SafeShellOutputWriter passes text and SGR colors but suppresses terminal
// control sequences that would corrupt the prompt or redraw the display.
type SafeShellOutputWriter struct {
	w     io.Writer
	state uint8
	csi   []byte
}

func NewSafeShellOutputWriter(w io.Writer) *SafeShellOutputWriter {
	return &SafeShellOutputWriter{w: w}
}

func (s *SafeShellOutputWriter) Write(data []byte) (int, error) {
	var out bytes.Buffer
	for _, b := range data {
		switch s.state {
		case 0:
			switch {
			case b == 0x1b:
				s.state = 1
			case b == '\n' || b == '\r' || b == '\t' || b >= 0x20:
				out.WriteByte(b)
			}
		case 1:
			switch b {
			case '[':
				s.state = 2
				s.csi = s.csi[:0]
			case ']', 'P', '_', '^', 'X':
				s.state = 3
			default:
				s.state = 0
			}
		case 2:
			if b >= 0x40 && b <= 0x7e {
				if b == 'm' && len(s.csi) < 64 {
					out.WriteString("\x1b[")
					out.Write(s.csi)
					out.WriteByte('m')
				}
				s.state = 0
			} else if (b >= '0' && b <= '9') || b == ';' || b == ':' {
				if len(s.csi) < 64 {
					s.csi = append(s.csi, b)
				}
			} else {
				s.state = 4
			}
		case 3:
			if b == 0x07 {
				s.state = 0
			} else if b == 0x1b {
				s.state = 5
			}
		case 4:
			if b >= 0x40 && b <= 0x7e {
				s.state = 0
			}
		case 5:
			if b == '\\' || b == 0x07 {
				s.state = 0
			} else if b != 0x1b {
				s.state = 3
			}
		}
	}
	if out.Len() > 0 {
		if _, err := s.w.Write(out.Bytes()); err != nil {
			return 0, err
		}
	}
	return len(data), nil
}
