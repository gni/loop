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

	redrawBar := func() {
		if kiReader != nil {
			ui.GetUI().StateMu.Lock()
			ui.GetUI().DrawStatusBar(w, *theme)
			ui.GetUI().StateMu.Unlock()
		}
	}

	printTaskView := func(id string) error {
		id = a.ResolveTaskID(id)
		status, output, err := a.GetTaskStatus(id)
		if err != nil {
			return err
		}
		if cmd := a.GetTaskCommand(id); cmd != "" {
			fmt.Fprintf(w, "task %s (%s) status: %s\noutput:\n%s\n", id, cmd, status, output)
		} else {
			fmt.Fprintf(w, "task %s status: %s\noutput:\n%s\n", id, status, output)
		}
		return nil
	}

	sub := parts[1]
	switch sub {
	case "view", "status", "info":
		var id string
		if len(parts) >= 3 {
			id = parts[2]
		} else {
			id = a.GetLastRunningTaskId()
			if id == "" {
				tasks := a.ListTasks()
				switch len(tasks) {
				case 0:
					fmt.Fprintln(w, "no background tasks registered.")
					return
				case 1:
					id = tasks[0].ID
				default:
					fmt.Fprintln(w, "usage: /task view <id> | /task list")
					return
				}
			}
		}
		if err := printTaskView(id); err != nil {
			fmt.Fprintf(w, "error: %v\n", err)
		}

	case "stream":
		var id string
		if len(parts) < 3 {
			id = a.GetLastRunningTaskId()
			if id == "" {
				fmt.Fprintln(w, "usage: /task stream <id>")
				return
			}
		} else {
			id = parts[2]
		}
		a.ToggleStreaming(a.ResolveTaskID(id), w)

	case "kill", "stop":
		var id string
		if len(parts) >= 3 {
			id = parts[2]
		} else {
			id = a.GetLastRunningTaskId()
			if id == "" {
				if len(a.ListTasks()) == 0 {
					fmt.Fprintln(w, "no background tasks registered.")
				} else {
					fmt.Fprintln(w, "no running background tasks to kill. usage: /task kill <id> | /task kill all")
				}
				return
			}
		}
		if id == "all" {
			killed := a.KillAllTasks()
			fmt.Fprintf(w, "terminated %d running background task(s).\n", killed)
			redrawBar()
			return
		}
		id = a.ResolveTaskID(id)
		if err := a.KillTask(id); err != nil {
			fmt.Fprintf(w, "error: %v\n", err)
		} else {
			fmt.Fprintf(w, "task %s successfully terminated.\n", id)
			redrawBar()
		}

	case "remove", "delete":
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /task remove <id>")
			return
		}
		id := a.ResolveTaskID(parts[2])
		if err := a.RemoveTask(id); err != nil {
			fmt.Fprintf(w, "error: %v\n", err)
		} else {
			fmt.Fprintf(w, "task %s removed from registry.\n", id)
			redrawBar()
		}

	case "clear", "clean":
		n := a.ClearTasks()
		fmt.Fprintf(w, "cleared %d background task(s).\n", n)
		redrawBar()

	default:
		if printTaskView(sub) != nil {
			fmt.Fprintln(w, "unknown task subcommand. usage: /task [list | view <id> | stream <id> | kill <id> | remove <id> | clear]")
		}
	}
}