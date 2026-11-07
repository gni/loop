package interactive

import (
	"bytes"
	"io"
	"testing"

	"loop/pkg/ui/style"
)

func TestSessionReaderFramesBatchedKeys(t *testing.T) {
	sr := NewSessionReader(bytes.NewBufferString("\x1b[B\x1b[B\r界"))
	defer sr.Close()
	for _, want := range []string{"\x1b[B", "\x1b[B", "\r", "界"} {
		got, _, err := sr.ReadKeyOrResize(nil)
		if err != nil || string(got) != want {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
	if _, _, err := sr.ReadKeyOrResize(nil); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestSessionReaderPreservesShortReads(t *testing.T) {
	sr := NewSessionReader(bytes.NewBufferString("\x1b[Bhello"))
	defer sr.Close()
	var out bytes.Buffer
	p := make([]byte, 1)
	for {
		n, err := sr.Read(p)
		out.Write(p[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if out.String() != "\x1b[Bhello" {
		t.Fatalf("lost bytes: %q", out.String())
	}
}

func TestSelectNavigatesAndEntersBatchedInput(t *testing.T) {
	sr := NewSessionReader(bytes.NewBufferString("\x1b[B\r"))
	defer sr.Close()
	var out bytes.Buffer
	got, err := RunInteractiveSelect(sr, nil, &out, "sessions", []string{"first", "second"}, style.GetTheme("plain"))
	if err != nil || got != 1 {
		t.Fatalf("selected %d, %v; want second session", got, err)
	}
}

func TestSelectEmpty(t *testing.T) {
	if _, err := RunInteractiveSelect(nil, nil, io.Discard, "sessions", nil, style.GetTheme("plain")); err == nil {
		t.Fatal("expected empty selection error")
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestSessionReaderPreservesInputError(t *testing.T) {
	want := io.ErrUnexpectedEOF
	sr := NewSessionReader(failingReader{want})
	defer sr.Close()
	if _, _, err := sr.ReadKeyOrResize(nil); err != want {
		t.Fatalf("got %v, want %v", err, want)
	}
}

func TestSelectApplicationCursorKeys(t *testing.T) {
	sr := NewSessionReader(bytes.NewBufferString("\x1bOB\r"))
	defer sr.Close()
	got, err := RunInteractiveSelect(sr, nil, io.Discard, "sessions", []string{"first", "second"}, style.GetTheme("plain"))
	if err != nil || got != 1 {
		t.Fatalf("selected %d, %v", got, err)
	}
}
