package agent

import (
	"bytes"
	"fmt"
	"io"
	"loop/pkg/domain/task"
	"loop/pkg/ui/style"
)

type EngineRenderer interface {
	ToolHeader(w io.Writer, theme style.UITheme, toolName, args string)
	ToolOutput(w io.Writer, theme style.UITheme, output string, isError bool, toolName, args string, bodyStreamed bool)
}

type mainRenderer struct{ a *Agent }

func (r *mainRenderer) ToolHeader(w io.Writer, theme style.UITheme, toolName, args string) {
	if r.a != nil && r.a.UI != nil {
		r.a.UI.RenderToolHeader(w, theme, toolName, args)
	} else {
		fmt.Fprintf(w, "tool call: %s %s\n", toolName, args)
	}
}

func (r *mainRenderer) ToolOutput(w io.Writer, theme style.UITheme, output string, isError bool, toolName, args string, bodyStreamed bool) {
	if toolName == "task_state" && !isError && !task.ShowProgressNotice(output, args) {
		return
	}
	if r.a != nil && r.a.UI != nil {
		collapse := r.a.Config != nil && r.a.Config.CollapseResults
		var rendered bytes.Buffer
		r.a.UI.RenderToolOutput(&rendered, output, isError, collapse, theme, toolName, args, bodyStreamed)
		if rendered.Len() == 0 {
			return
		}
		if counter, ok := w.(*NewlineCounterWriter); ok {
			counter.EnsureNewline()
		}
		_, _ = rendered.WriteTo(w)
	} else if !bodyStreamed {
		fmt.Fprintln(w, output)
	}
}

func NewMainRenderer(a *Agent) EngineRenderer { return &mainRenderer{a: a} }
