package agent

import (
	"bytes"
	"io"
	"testing"

	"loop/pkg/db"
	"loop/pkg/ui/style"
)

type headerProbeRenderer struct{ calls int }

func (r *headerProbeRenderer) ToolHeader(io.Writer, style.UITheme, string, string) {
	r.calls++
}

func (r *headerProbeRenderer) ToolOutput(io.Writer, style.UITheme, string, bool, string, string, bool) {
}

type trackedTitleStream struct {
	StreamRenderer
	printed bool
}

func (s trackedTitleStream) GetToolTitleLineNumber(int) int {
	if s.printed {
		return 0
	}
	return -1
}

func TestParallelToolHeaderNotRenderedTwice(t *testing.T) {
	e := &TurnEngine{}
	call := db.ToolCall{Function: db.ToolFunction{Name: "read", Arguments: `{"path":"a.txt"}`}}
	for _, tc := range []struct {
		alreadyPrinted bool
		want           int
	}{
		{alreadyPrinted: true, want: 0},
		{alreadyPrinted: false, want: 1},
	} {
		renderer := &headerProbeRenderer{}
		e.ensureToolHeaderRendered(0, call, "", true, trackedTitleStream{printed: tc.alreadyPrinted}, &bytes.Buffer{}, style.GetTheme("plain"), renderer)
		if renderer.calls != tc.want {
			t.Fatalf("alreadyPrinted=%t: got %d headers, want %d", tc.alreadyPrinted, renderer.calls, tc.want)
		}
	}
}
