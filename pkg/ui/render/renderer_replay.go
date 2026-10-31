package render

import (
	"fmt"
	"io"
	"strings"
	"time"

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
	if n > 0 && sr.liveReplayEvents[n-1].kind == kind {
		sr.liveReplayEvents[n-1].text += text
		return
	}
	sr.liveReplayEvents = append(sr.liveReplayEvents, liveReplayEvent{kind: kind, text: text})
}

type liveReplayCall struct {
	name, path, content, result string
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
			result: v.result, bytes: v.receivedBytes, index: i,
			completed: v.completed, failed: v.failed,
			arguments: v.arguments, output: v.output, resultRecorded: v.resultRecorded,
		}
	}
	inThinking := sr.inThinking
	textFlushed := sr.liveReplayTextFlushed
	markdownEnabled := sr.markdownEnabled
	activeCall := sr.liveActiveCall
	showMicroseconds := sr.showMicroseconds
	sr.mu.Unlock()

	for i, ev := range events {
		last := i == len(events)-1
		switch ev.kind {
		case "reasoning":
			if collapsed {
				fmt.Fprintln(w, FormatThinkingSummary(theme, ev.duration, last && inThinking, ThinkingLineCount(ev.text), showMicroseconds))
				if !last {
					fmt.Fprintln(w)
				}
				continue
			}
			fmt.Fprint(w, style.NewStyle().Foreground(theme.Border).Italic(true).Render(ev.text))
			if !last || !inThinking {
				if !strings.HasSuffix(ev.text, "\n") {
					fmt.Fprintln(w)
				}
				label := "thought"
				if ev.duration > 0 {
					label = "thought (" + style.FormatElapsedDuration(time.Duration(ev.duration*float64(time.Second)), showMicroseconds) + ")"
				}
				fmt.Fprintln(w, style.NewStyle().Foreground(theme.Border).Italic(true).Render(label))
				if !last {
					fmt.Fprintln(w)
				}
			}
		case "text":
			if markdownEnabled {
				// Use the streaming parser without finalizing an active line.
				// Buffered fences and inline delimiters must remain buffered
				// until the original renderer receives their next fragment.
				live := NewLiveMarkdownRenderer(w, theme)
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
			if i > 0 && events[i-1].kind != "reasoning" {
				fmt.Fprintln(w)
			}
			status := ToolStatusPending
			if call.completed {
				status = ToolStatusSuccess
				if call.failed {
					status = ToolStatusError
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
					command = ExtractToolTarget(call.name, call.arguments)
				}
				fmt.Fprint(w, FormatBashCommandLine(RenderToolSymbol(call.name, status, theme), command, theme))
				if !last || ev.index != activeCall || call.completed {
					fmt.Fprintln(w)
				}
			} else {
				fmt.Fprintln(w, FormatToolTitle(RenderToolSymbol(call.name, status, theme), call.name, path, theme))
			}
			if call.resultRecorded {
				RenderToolOutput(w, call.output, call.failed, collapsed, theme, call.name, call.arguments, false)
				continue
			}
			if call.name == "bash" {
				continue
			}
			if collapsed && !call.failed && (call.name == "write" || call.name == "edit") {
				// Match the compact live progress format; never emit source.
				tail := []rune(safeLiveLabel(strings.Join(strings.Fields(call.content), " ")))
				maxExcerpt := 72
				if width, _ := style.GetTerminalSize(); width > 0 && width < 100 {
					maxExcerpt = width - 28
				}
				if maxExcerpt < 0 {
					maxExcerpt = 0
				}
				if len(tail) > maxExcerpt {
					tail = append([]rune("…"), tail[len(tail)-maxExcerpt:]...)
				}
				label := fmt.Sprintf("↳ receiving %s #%d (%d bytes)", call.name, call.index+1, call.bytes)
				if call.name == "write" && len(tail) > 0 && maxExcerpt > 0 {
					label = fmt.Sprintf("↳ receiving write #%d: %s", call.index+1, string(tail))
				}
				if !call.completed {
					fmt.Fprintln(w, style.NewStyle().Foreground(theme.Border).Render(label))
				}
			} else if call.content != "" {
				if call.name == "write" {
					lang := DetectLangFromPath(call.path)
					if lang == "plaintext" {
						lang = DetectLangFromContent(call.content)
					}
					var highlighted strings.Builder
					if err := HighlightWithoutTrailingNewline(&highlighted, call.content, lang, theme.ChromaStyle); err == nil && highlighted.Len() > 0 {
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
