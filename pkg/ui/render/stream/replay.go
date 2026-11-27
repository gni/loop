package stream

import (
	"fmt"
	"io"
	"strings"
	"time"

	"loop/pkg/domain/task"
	"loop/pkg/ui/render"
	"loop/pkg/ui/render/markdown"
	"loop/pkg/ui/render/syntax"
	"loop/pkg/ui/style"
)

// liveReplayEvent is logical output from the current, uncommitted inference.
// Tool bodies are stored once per tool index, independently of how many times
// Ctrl+O changes their presentation. No terminal cursor commands are saved.
type liveReplayEvent struct {
	kind     string // reasoning, text or tool
	text     string
	index    int
	duration float64
}

func (sr *StreamRenderer) appendLiveReplayTextLocked(kind, text string) {
	if text == "" {
		return
	}
	n := len(sr.liveReplayEvents)
	if n == 0 || sr.liveReplayEvents[n-1].kind != kind {
		sr.liveReplayText.Reset()
		sr.liveReplayEvents = append(sr.liveReplayEvents, liveReplayEvent{kind: kind})
	}
	sr.liveReplayText.WriteString(text)
	// Builder snapshots share an immutable prefix. Appending future fragments
	// preserves snapshots already being replayed without copying the whole reply.
	sr.liveReplayEvents[len(sr.liveReplayEvents)-1].text = sr.liveReplayText.String()
}

type liveReplayCall struct {
	name, path, content, result string
	editDiff                    string
	arguments, output           string
	bytes, index                int
	completed, failed           bool
	resultRecorded              bool
}

// ReplayActiveTurn reconstructs only the current inference. Completed
// inferences come from committed history; model fragments and in-flight
// writes come from semantic renderer state, never from ANSI output bytes.
func (sr *StreamRenderer) ReplayActiveTurn(w io.Writer, collapsed bool, theme style.UITheme) {
	sr.mu.Lock()
	events := append([]liveReplayEvent(nil), sr.liveReplayEvents...)
	calls := make(map[int]liveReplayCall, len(sr.liveCallViews))
	for i, v := range sr.liveCallViews {
		calls[i] = liveReplayCall{
			name: v.name, path: v.path, content: v.fullContent.String(),
			editDiff: v.editDiff,
			result:   v.result, bytes: v.receivedBytes, index: i,
			completed: v.completed, failed: v.failed,
			arguments: v.arguments, output: v.output, resultRecorded: v.resultRecorded,
		}
	}
	inThinking := sr.inThinking
	textFlushed := sr.liveReplayTextFlushed
	markdownEnabled := sr.markdownEnabled
	bashSyntaxColor := sr.bashSyntaxColor
	activeCall := sr.liveActiveCall
	showMicroseconds := sr.showMicroseconds
	sr.mu.Unlock()

	thinkingStyle := markdown.GetThinkingStyle(theme)
	viewportWidth := markdown.RenderWidth(w)

	for i, ev := range events {
		last := i == len(events)-1
		switch ev.kind {
		case "reasoning":
			if collapsed {
				fmt.Fprintln(w, render.FormatThinkingSummary(theme, ev.duration, last && inThinking, render.ThinkingLineCount(ev.text), showMicroseconds))
				if !last {
					fmt.Fprintln(w)
				}
				continue
			}
			if markdownEnabled {
				live := markdown.NewLiveThinkingMarkdownRenderer(w, theme)
				live.Write(ev.text)
				if !last || !inThinking {
					live.EnsureTrailingNewline()
					label := "thought"
					if ev.duration > 0 {
						label = "thought (" + style.FormatElapsedDuration(time.Duration(ev.duration*float64(time.Second)), showMicroseconds) + ")"
					}
					fmt.Fprintln(w, thinkingStyle.Render(label))
					if !last {
						fmt.Fprintln(w)
					}
				}
				continue
			}
			markdown.RenderRawReasoningContent(w, ev.text, theme)
			if !last || !inThinking {
				if !strings.HasSuffix(ev.text, "\n") {
					fmt.Fprintln(w)
				}
				label := "thought"
				if ev.duration > 0 {
					label = "thought (" + style.FormatElapsedDuration(time.Duration(ev.duration*float64(time.Second)), showMicroseconds) + ")"
				}
				fmt.Fprintln(w, thinkingStyle.Render(label))
				if !last {
					fmt.Fprintln(w)
				}
			}
		case "text":
			if markdownEnabled {
				// Use the streaming parser without finalizing an active line.
				// Buffered fences and inline delimiters must remain buffered
				// until the original renderer receives their next fragment.
				live := markdown.NewLiveMarkdownRenderer(w, theme)
				live.Write(ev.text)
				if !last {
					live.EnsureTrailingNewline()
				} else if textFlushed {
					live.Flush()
				}
			} else {
				fmt.Fprint(w, ev.text)
				if !last && !strings.HasSuffix(ev.text, "\n") {
					fmt.Fprintln(w)
				}
			}
		case "tool":
			call, ok := calls[ev.index]
			if !ok {
				continue
			}
			if call.name == "task_state" {
				if call.resultRecorded && (call.failed || task.ShowProgressNotice(call.output, call.arguments)) {
					render.RenderToolOutputWithOptions(w, call.output, call.failed, collapsed, theme, call.name, call.arguments, false, bashSyntaxColor)
				}
				continue
			}
			if i > 0 && events[i-1].kind != "reasoning" {
				fmt.Fprintln(w)
			}
			status := render.ToolStatusPending
			if call.completed {
				status = render.ToolStatusSuccess
				if call.failed {
					status = render.ToolStatusError
				}
			}
			path := call.path
			if path == "" {
				path = "…"
			}
			if call.name == "bash" {
				// Command text was displayed inline after the shell prompt.
				command := call.content
				if command == "" && call.arguments != "" {
					command = render.ExtractToolTarget(call.name, call.arguments)
				}
				fmt.Fprint(w, render.FormatBashCommandLineStyled(render.RenderToolSymbol(call.name, status, theme), command, theme, bashSyntaxColor))
				hasTrailingNL := strings.HasSuffix(strings.ReplaceAll(command, "\r", "\n"), "\n") && strings.TrimSpace(command) != ""
				if !last || ev.index != activeCall || call.completed || hasTrailingNL {
					fmt.Fprintln(w)
				}
			} else {
				fmt.Fprintln(w, render.FormatToolTitleForWidth(render.RenderToolSymbol(call.name, status, theme), call.name, path, theme, viewportWidth))
			}
			if call.resultRecorded {
				render.RenderToolOutputWithOptions(w, call.output, call.failed, collapsed, theme, call.name, call.arguments, false, bashSyntaxColor)
				continue
			}
			if call.name == "bash" {
				continue
			}
			if call.name == "edit" && !collapsed && call.editDiff != "" {
				for _, line := range render.FormatEditDiffLines(call.editDiff, theme) {
					fmt.Fprintln(w, line)
				}
			} else if !call.failed && ((collapsed && call.name == "write") || call.name == "edit") {
				// Match the compact live progress format while receiving.
				clean := safeLiveLabel(strings.Join(strings.Fields(call.content), " "))
				maxExcerpt := 72
				if viewportWidth > 0 && viewportWidth < 100 {
					maxExcerpt = max(0, viewportWidth-28)
				}
				clean = render.TailCells(clean, maxExcerpt)
				label := fmt.Sprintf("↳ receiving %s #%d (%d bytes)", call.name, call.index+1, call.bytes)
				if (call.name == "write" || call.name == "edit") && clean != "" && maxExcerpt > 0 {
					label = fmt.Sprintf("↳ receiving %s #%d: %s", call.name, call.index+1, clean)
				}
				if !call.completed {
					fmt.Fprintln(w, style.NewStyle().Foreground(theme.TextMuted).Render(label))
				}
			} else if call.content != "" {
				if call.name == "write" {
					lang := DetectLangFromPath(call.path)
					if lang == "plaintext" {
						lang = DetectLangFromContent(call.content)
					}
					var highlighted strings.Builder
					if err := syntax.HighlightWithoutTrailingNewline(&highlighted, call.content, lang, theme.ChromaStyle); err == nil && highlighted.Len() > 0 {
						fmt.Fprint(w, highlighted.String())
					} else {
						fmt.Fprint(w, call.content)
					}
				} else {
					fmt.Fprint(w, call.content)
				}
				if !strings.HasSuffix(call.content, "\n") && (!last || ev.index != activeCall || call.completed) {
					fmt.Fprintln(w)
				}
			}
			if call.result != "" && !strings.Contains(call.result, "details below") {
				fmt.Fprintln(w, call.result)
			}
		}
	}
}
