package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/domain/session"
)

func handleExportCommand(a *agent.Agent, parts []string, messages *[]db.Message, currentSessionID *string, w io.Writer) {
	if messages == nil || len(*messages) == 0 {
		fmt.Fprintln(w, "no conversation history to export.")
		return
	}

	sessionID := ""
	if currentSessionID != nil {
		sessionID = *currentSessionID
	}

	targetPath := ""
	if len(parts) > 1 {
		targetPath = strings.TrimSpace(strings.Join(parts[1:], " "))
	}

	timestamp := time.Now().Format("2006-01-02-150405")
	shortID := "session"
	if len(sessionID) >= 8 {
		shortID = sessionID[:8]
	}

	if targetPath == "" {
		targetPath = fmt.Sprintf("session-%s-%s.md", shortID, timestamp)
	}

	workspaceRoot := "."
	if a != nil && a.WorkspaceRoot != "" {
		workspaceRoot = a.WorkspaceRoot
	}
	if a != nil {
		safe, err := a.SafePath(targetPath)
		if err != nil {
			fmt.Fprintf(w, "cannot export to target path: %v\n", err)
			return
		}
		targetPath = safe
	} else if !filepath.IsAbs(targetPath) {
		targetPath = filepath.Join(workspaceRoot, targetPath)
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		fmt.Fprintf(w, "failed to create directory for export: %v\n", err)
		return
	}

	ext := strings.ToLower(filepath.Ext(targetPath))
	if ext == ".jsonl" {
		err := writeExportFile(targetPath, func(w io.Writer) error {
			return encodeJSONLTranscript(w, sessionID, workspaceRoot, *messages)
		})
		if err != nil {
			fmt.Fprintf(w, "cannot export JSONL transcript: %v\n", err)
			return
		}
		fmt.Fprintf(w, "Exported %d message(s) to JSONL: %s\n", len(*messages), targetPath)
		return
	}

	// Default: Markdown export
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Conversation Session Export: %s\n\n", sessionID)
	fmt.Fprintf(&sb, "- **Date**: %s\n", time.Now().Format(time.RFC1123))
	if a != nil && a.Config != nil && a.Config.Model != "" {
		fmt.Fprintf(&sb, "- **Model**: %s\n", a.Config.Model)
	}
	if a != nil && a.WorkspaceRoot != "" {
		fmt.Fprintf(&sb, "- **Workspace**: %s\n", a.WorkspaceRoot)
	}
	fmt.Fprintf(&sb, "- **Messages**: %d\n\n---\n\n", len(*messages))

	for idx, m := range *messages {
		switch m.Role {
		case "system":
			fmt.Fprintf(&sb, "#### ⚙️ System Prompt (Turn %d)\n\n", idx+1)
			sb.WriteString("<details><summary>View System Prompt</summary>\n\n```text\n")
			sb.WriteString(m.Content)
			sb.WriteString("\n```\n</details>\n\n---\n\n")

		case "user":
			fmt.Fprintf(&sb, "### 👤 User (Turn %d)\n\n", idx+1)
			sb.WriteString(m.Content)
			sb.WriteString("\n\n---\n\n")

		case "assistant":
			fmt.Fprintf(&sb, "### 🤖 Assistant (Turn %d)\n\n", idx+1)
			if m.ReasoningContent != "" {
				sb.WriteString("<details><summary>Thinking Process</summary>\n\n```text\n")
				sb.WriteString(m.ReasoningContent)
				sb.WriteString("\n```\n</details>\n\n")
			}
			if m.Content != "" {
				sb.WriteString(m.Content)
				sb.WriteString("\n\n")
			}
			if len(m.ToolCalls) > 0 {
				sb.WriteString("#### Tool Calls:\n")
				for _, tc := range m.ToolCalls {
					fmt.Fprintf(&sb, "- **`%s`** (id: `%s`)\n", tc.Function.Name, tc.ID)
					sb.WriteString("  ```json\n  ")
					sb.WriteString(strings.ReplaceAll(tc.Function.Arguments, "\n", "\n  "))
					sb.WriteString("\n  ```\n")
				}
				sb.WriteString("\n")
			}
			sb.WriteString("---\n\n")

		case "tool":
			fmt.Fprintf(&sb, "#### 🛠️ Tool Result: `%s`\n\n", m.Name)
			sb.WriteString("<details><summary>Output Details</summary>\n\n```text\n")
			sb.WriteString(m.Content)
			sb.WriteString("\n```\n</details>\n\n---\n\n")
		}
	}

	if err := writeExportFile(targetPath, func(w io.Writer) error {
		_, err := io.WriteString(w, sb.String())
		return err
	}); err != nil {
		fmt.Fprintf(w, "failed to write export file: %v\n", err)
		return
	}

	relPath, err := filepath.Rel(workspaceRoot, targetPath)
	if err == nil && !strings.HasPrefix(relPath, "..") {
		targetPath = relPath
	}
	fmt.Fprintf(w, "Exported session transcript to %s\n", targetPath)
}

// JSONL transcripts use the same envelope as session import. Encoding bare
// Message values would silently lose their content when decoded as Records.
func encodeJSONLTranscript(w io.Writer, sessionID, workspace string, messages []db.Message) error {
	if sessionID == "" {
		sessionID = session.NewUUID()
	}
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	encoder := json.NewEncoder(w)
	header := session.Record{Type: "session", Version: session.CurrentSessionVersion,
		ID: sessionID, Cwd: workspace, Timestamp: timestamp}
	if err := encoder.Encode(header); err != nil {
		return err
	}
	parentID := ""
	for _, msg := range messages {
		record := session.Record{Type: "message", ID: session.NewUUID(), ParentID: parentID,
			Timestamp: timestamp, Message: msg}
		if err := encoder.Encode(record); err != nil {
			return err
		}
		parentID = record.ID
	}
	return nil
}

// Write beside the destination and replace only after successful encoding and
// close, so an interrupted export cannot truncate an existing transcript.
func writeExportFile(path string, write func(io.Writer) error) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".loop-export-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	writeErr := write(file)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
