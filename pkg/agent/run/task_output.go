package run

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"loop/pkg/domain/tool"
)

// ReadBackgroundTaskOutput serializes each process snapshot with its cursor
// update. Independent stdout/stderr offsets remain valid when either tail rolls
// or an identical line is emitted again.
func ReadBackgroundTaskOutput(ctx context.Context, id string, full bool, provider tool.TaskOutputSnapshotProvider) (*tool.BackgroundTaskSnapshot, string, error) {
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state != nil {
		state.processMu.Lock()
		defer state.processMu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	snapshot, err := provider.GetTaskOutputSnapshot(id, full)
	if err != nil || snapshot == nil {
		return snapshot, "", err
	}
	if snapshot.StdoutBytes < int64(len(snapshot.Stdout)) || snapshot.StderrBytes < int64(len(snapshot.Stderr)) {
		return nil, "", fmt.Errorf("background task %s returned invalid output positions", id)
	}
	key := snapshot.ID
	if key == "" {
		key = id
	}
	var previous processOutputCursor
	if state != nil {
		previous = state.processOutput[key]
	}
	seen := previous.streams && previous.startedAt == snapshot.StartedAt && previous.stdout <= snapshot.StdoutBytes && previous.stderr <= snapshot.StderrBytes
	// Do not acknowledge an incomplete trailing UTF-8 sequence while the task is
	// still running, so the next snapshot can deliver the complete rune.
	incomplete := snapshot.Status == "running" && (hasIncompleteTrailingUTF8(snapshot.Stdout) || hasIncompleteTrailingUTF8(snapshot.Stderr))
	valid := !incomplete
	if !valid {
		seen = false
	}
	stdout := backgroundStreamDelta(snapshot.Stdout, snapshot.StdoutBytes, previous.stdout, seen && !full)
	stderr := backgroundStreamDelta(snapshot.Stderr, snapshot.StderrBytes, previous.stderr, seen && !full)
	var output strings.Builder
	output.WriteString(stdout)
	if stdout != "" && stderr != "" && !strings.HasSuffix(stdout, "\n") {
		output.WriteByte('\n')
	}
	output.WriteString(stderr)
	if snapshot.Diagnostic != "" {
		if output.Len() > 0 && !strings.HasSuffix(output.String(), "\n") {
			output.WriteByte('\n')
		}
		fmt.Fprintf(&output, "[task error: %s]", snapshot.Diagnostic)
	}
	text := output.String()
	if text == "" {
		text = "(no output)"
		if seen && !full {
			text = "(no new output)"
		}
	} else if seen && !full && (stdout != "" || stderr != "") {
		text = "[New output since previous status]\n" + text
	}
	if state != nil {
		if valid {
			state.rememberProcessOutput(key, processOutputCursor{stdout: snapshot.StdoutBytes, stderr: snapshot.StderrBytes, startedAt: snapshot.StartedAt, streams: true})
		} else {
			delete(state.processOutput, key)
		}
	}
	return snapshot, text, nil
}

func hasIncompleteTrailingUTF8(s string) bool {
	for back := 1; back <= 3 && back <= len(s); back++ {
		start := len(s) - back
		if utf8.RuneStart(s[start]) {
			return !utf8.FullRuneInString(s[start:])
		}
	}
	return false
}

func backgroundStreamDelta(text string, total, previous int64, seen bool) string {
	start := total - int64(len(text))
	skipped := start
	if seen {
		if previous >= total {
			return ""
		}
		skipped = max(0, start-previous)
		index := int(max(0, previous-start))
		for index < len(text) && !utf8.RuneStart(text[index]) {
			index++
			skipped++
		}
		text = text[index:]
	}
	if skipped > 0 {
		return fmt.Sprintf("...(truncated %d bytes)...\n", skipped) + text
	}
	return text
}

// processMu protects this bounded cursor map. Eviction may repeat output once
// but must never suppress new data.
func (state *taskState) rememberProcessOutput(id string, next processOutputCursor) {
	if state.processOutput == nil {
		state.processOutput = make(map[string]processOutputCursor)
	}
	if _, exists := state.processOutput[id]; !exists && len(state.processOutput) >= 1024 {
		for key := range state.processOutput {
			delete(state.processOutput, key)
			break
		}
	}
	state.processOutput[id] = next
}
