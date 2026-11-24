package commands

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"loop/pkg/db"
	"loop/pkg/terminal"
)

func handleCopyCommand(messages *[]db.Message, w io.Writer) {
	if messages == nil {
		fmt.Fprintln(w, "No conversation history found to copy.")
		return
	}
	var content string
	for index := len(*messages) - 1; index >= 0; index-- {
		msg := (*messages)[index]
		if msg.Role == "assistant" && strings.TrimSpace(msg.Content) != "" {
			content = msg.Content
			break
		}
	}
	if content == "" {
		fmt.Fprintln(w, "No assistant response found to copy.")
		return
	}

	// OSC 52 also supports remote terminals. Sending it is a request, not an
	// acknowledgement that a terminal accepted the clipboard contents.
	terminalErr := terminal.WriteOSC52(w, content)
	desktopCopied := terminal.CopyToDesktopClipboard(content)
	switch {
	case desktopCopied:
		fmt.Fprintf(w, "Copied last assistant response (%d characters) to clipboard.\n", utf8.RuneCountInString(content))
	case terminalErr == nil:
		fmt.Fprintf(w, "Sent OSC 52 clipboard request for last assistant response (%d characters). If your terminal ignores OSC 52, press Ctrl+X (or run /select) for native terminal copy.\n", utf8.RuneCountInString(content))
	default:
		fmt.Fprintf(w, "Cannot copy response: %v\n", terminalErr)
	}
}
