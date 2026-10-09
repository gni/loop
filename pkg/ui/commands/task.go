package commands

import (
	"fmt"
	"io"
	"time"

	"loop/pkg/agent"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

func handleTaskCommand(a *agent.Agent, parts []string, theme *style.UITheme, w io.Writer, kiReader *interceptor.KeyInterceptorReader) {
	if len(parts) < 2 || parts[1] == "list" {
		tasks := a.ListTasks()
		if len(tasks) == 0 {
			fmt.Fprintln(w, "no background tasks registered.")
			return
		}
		fmt.Fprintln(w, "background tasks:")
		for _, t := range tasks {
			fmt.Fprintf(w, "  - %s: %s (duration: %v, output size: %d bytes) - `%s`\n",
				t.ID, t.Status, t.Duration.Round(time.Millisecond), t.BytesOut, t.Command)
		}
		return
	}
	sub := parts[1]
	switch sub {
	case "view":
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /task view <id>")
			return
		}
		id := parts[2]
		status, output, err := a.GetTaskStatus(id)
		if err != nil {
			fmt.Fprintf(w, "error: %v\n", err)
			return
		}
		fmt.Fprintf(w, "task %s status: %s\noutput:\n%s\n", id, status, output)
	case "stream":
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /task stream <id>")
			return
		}
		id := parts[2]
		a.ToggleStreaming(id, w)
	case "kill", "remove":
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /task kill <id> | /task kill all")
			return
		}
		id := parts[2]
		if id == "all" {
			killed := a.KillAllTasks()
			fmt.Fprintf(w, "terminated %d running background task(s).\n", killed)
			if kiReader != nil {
				ui.GetUI().StateMu.Lock()
				ui.GetUI().DrawStatusBar(w, *theme)
				ui.GetUI().StateMu.Unlock()
			}
			return
		}
		err := a.KillTask(id)
		if err != nil {
			fmt.Fprintf(w, "error: %v\n", err)
		} else {
			fmt.Fprintf(w, "task %s successfully terminated.\n", id)
		}
	default:
		fmt.Fprintln(w, "unknown task subcommand. usage: /task [list | view <id> | stream <id> | kill <id>]")
	}
}
