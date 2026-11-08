package commands

import (
	"fmt"
	"io"
	"loop/pkg/ui/render"

	"loop/pkg/agent"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

func handleTaskCommand(a *agent.Agent, parts []string, theme *style.UITheme, w io.Writer, kiReader *interceptor.KeyInterceptorReader) {
	if len(parts) < 2 || parts[1] == "list" {
		render.RenderBackgroundTasks(w, a.ListTasks(), *theme)
		return
	}

	redrawBar := func() {
		if kiReader != nil {
			activeCount := a.CountActiveTasks()
			ui.GetUI().StateMu.Lock()
			ui.GetUI().State.ActiveTasksCount = activeCount
			ui.GetUI().StateMu.Unlock()
			ui.GetUI().DrawStatusBar(w, *theme)
		}
	}

	printTaskView := func(id string) error {
		id = a.ResolveTaskID(id)
		status, output, err := a.GetFullTaskStatus(id)
		if err != nil {
			return err
		}
		render.RenderBackgroundTask(w, id, a.GetTaskCommand(id), status, output, false, *theme)
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
