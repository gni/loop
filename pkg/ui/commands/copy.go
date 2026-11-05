package commands

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"loop/pkg/db"
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
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	_, terminalErr := fmt.Fprintf(w, "\x1b]52;c;%s\x07", encoded)
	desktopCopied := copyToDesktopClipboard(content)
	switch {
	case desktopCopied:
		fmt.Fprintf(w, "Copied last assistant response (%d characters) to clipboard.\n", utf8.RuneCountInString(content))
	case terminalErr == nil:
		fmt.Fprintf(w, "Sent clipboard request for last assistant response (%d characters).\n", utf8.RuneCountInString(content))
	default:
		fmt.Fprintf(w, "Cannot copy response: %v\n", terminalErr)
	}
}

func copyToDesktopClipboard(content string) bool {
	var candidates [][]string
	switch runtime.GOOS {
	case "darwin":
		candidates = [][]string{{"pbcopy"}}
	case "windows":
		candidates = [][]string{{"clip.exe"}}
	default:
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			candidates = append(candidates, []string{"wl-copy"})
		}
		if os.Getenv("DISPLAY") != "" {
			candidates = append(candidates, []string{"xclip", "-selection", "clipboard"}, []string{"xsel", "-b", "-i"})
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate[0])
		if err != nil {
			continue
		}
		cmd := exec.CommandContext(ctx, path, candidate[1:]...)
		cmd.Stdin = strings.NewReader(content)
		cmd.WaitDelay = 250 * time.Millisecond
		if cmd.Run() == nil {
			return true
		}
	}
	return false
}
