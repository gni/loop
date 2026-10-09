package agent

import (
	"fmt"
	"io"

	"loop/pkg/ui/style"
)

type NewlineCounterWriter struct {
	io.Writer
	count int
	col   int
	inEsc bool
	inCSI bool
}

func NewNewlineCounterWriter(w io.Writer) *NewlineCounterWriter {
	return &NewlineCounterWriter{Writer: w}
}

func (n *NewlineCounterWriter) Write(p []byte) (int, error) {
	termW, _ := style.GetTerminalSize()
	if termW <= 0 {
		termW = 80
	}

	for _, b := range p {
		if n.inEsc {
			if b == '[' {
				n.inCSI = true
				n.inEsc = false
			} else {
				n.inEsc = false
			}
			continue
		}
		if b == '\x1b' {
			n.inEsc = true
			continue
		}
		if n.inCSI {
			if b >= 0x40 && b <= 0x7E {
				n.inCSI = false
			}
			continue
		}

		if b == '\n' {
			n.count++
			n.col = 0
		} else if b == '\r' {
			n.col = 0
		} else if (b >= 32 && b < 127) || b >= 0xC0 {
			n.col++
			if n.col >= termW {
				n.count++
				n.col = 0
			}
		}
	}
	return n.Writer.Write(p)
}

func (n *NewlineCounterWriter) GetCount() int {
	return n.count
}

func (n *NewlineCounterWriter) Unwrap() io.Writer {
	return n.Writer
}

type FallbackStreamRenderer struct {
	w io.Writer
}

func NewFallbackStreamRenderer(w io.Writer) *FallbackStreamRenderer {
	return &FallbackStreamRenderer{w: w}
}

func (f *FallbackStreamRenderer) Write(content string) {
	fmt.Fprint(f.w, content)
}
func (f *FallbackStreamRenderer) WriteReasoning(content string)                    {}
func (f *FallbackStreamRenderer) Flush()                                           {}
func (f *FallbackStreamRenderer) HasOutput() bool                                  { return false }
func (f *FallbackStreamRenderer) StartToolCall(toolName string, toolCallIndex int) {}
func (f *FallbackStreamRenderer) WriteToolCall(content string)                     {}
func (f *FallbackStreamRenderer) GetToolTitleLineNumber(index int) int             { return -1 }
func (f *FallbackStreamRenderer) DidStreamToolBody(index int) bool                 { return false }
func (f *FallbackStreamRenderer) CompleteToolCall(index int, toolName string, toolArgs string, isError bool) {
}
func (f *FallbackStreamRenderer) GetReasoningDuration() float64 { return 0 }
func (f *FallbackStreamRenderer) SetPrompt(prompt string)       {}

type CustomTeeWriter struct {
	screen io.Writer
	buffer io.Writer
}

func NewCustomTeeWriter(screen, buffer io.Writer) *CustomTeeWriter {
	return &CustomTeeWriter{screen: screen, buffer: buffer}
}

func (c *CustomTeeWriter) Write(p []byte) (n int, err error) {
	n, err = c.screen.Write(p)
	if err == nil {
		_, _ = c.buffer.Write(p)
	}
	return n, err
}

func (c *CustomTeeWriter) Unwrap() io.Writer {
	return c.screen
}

func UnwrapWriter(w io.Writer) io.Writer {
	type unwrapper interface {
		Unwrap() io.Writer
	}
	for {
		if u, ok := w.(unwrapper); ok {
			unwrapped := u.Unwrap()
			if unwrapped == nil || unwrapped == w {
				return w
			}
			w = unwrapped
		} else {
			return w
		}
	}
}

func unwrapWriter(w io.Writer) io.Writer {
	return UnwrapWriter(w)
}
