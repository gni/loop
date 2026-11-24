package commands

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/terminal"
	"loop/pkg/ui"
)

type fencedCodeBlock struct {
	Lang string
	Code string
}

// extractFencedCodeBlocks extracts CommonMark fenced code blocks (backtick or
// tilde fences, 3+ chars, up to 3 leading spaces) from Markdown text.
func extractFencedCodeBlocks(md string) []fencedCodeBlock {
	lines := strings.Split(md, "\n")
	var blocks []fencedCodeBlock
	inFence := false
	var fenceChar byte
	fenceLen := 0
	lang := ""
	var body []string

	for _, rawLine := range lines {
		line := strings.TrimSuffix(rawLine, "\r")
		if !inFence {
			trimmed := strings.TrimLeft(line, " ")
			indent := len(line) - len(trimmed)
			if indent <= 3 && len(trimmed) >= 3 && (trimmed[0] == '`' || trimmed[0] == '~') {
				ch := trimmed[0]
				count := 0
				for count < len(trimmed) && trimmed[count] == ch {
					count++
				}
				info := strings.TrimSpace(trimmed[count:])
				// CommonMark forbids backticks in info strings for backtick fences.
				if count >= 3 && (ch != '`' || !strings.ContainsRune(info, '`')) {
					inFence = true
					fenceChar = ch
					fenceLen = count
					fields := strings.Fields(info)
					if len(fields) > 0 {
						lang = fields[0]
					} else {
						lang = ""
					}
					body = body[:0]
					continue
				}
			}
			continue
		}

		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		if indent <= 3 && len(trimmed) >= fenceLen && trimmed[0] == fenceChar {
			count := 0
			for count < len(trimmed) && trimmed[count] == fenceChar {
				count++
			}
			if count >= fenceLen && strings.TrimSpace(trimmed[count:]) == "" {
				blocks = append(blocks, fencedCodeBlock{
					Lang: lang,
					Code: strings.Join(body, "\n"),
				})
				inFence = false
				body = body[:0]
				continue
			}
		}
		body = append(body, line)
	}

	if inFence && len(body) > 0 {
		blocks = append(blocks, fencedCodeBlock{
			Lang: lang,
			Code: strings.Join(body, "\n"),
		})
	}
	return blocks
}

func lastAssistantCodeBlocks(messages []db.Message) []fencedCodeBlock {
	for index := len(messages) - 1; index >= 0; index-- {
		msg := messages[index]
		if msg.Role != "assistant" || strings.TrimSpace(msg.Content) == "" {
			continue
		}
		if blocks := extractFencedCodeBlocks(msg.Content); len(blocks) > 0 {
			return blocks
		}
	}
	return nil
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func previewFirstLine(code string) string {
	for _, raw := range strings.Split(code, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) > 56 {
			runes := []rune(line)
			return string(runes[:55]) + "..."
		}
		return line
	}
	return "(empty)"
}

func deliverClipboardCopy(w io.Writer, content, label string) {
	rawW := agent.UnwrapWriter(w)
	var terminalErr error
	if rawW != nil {
		ui.TerminalMu.Lock()
		terminalErr = terminal.WriteOSC52(rawW, content)
		ui.TerminalMu.Unlock()
	}
	desktopCopied := terminal.CopyToDesktopClipboard(content)
	chars := utf8.RuneCountInString(content)
	lines := countLines(content)

	switch {
	case desktopCopied:
		fmt.Fprintf(w, "Copied %s (%d chars, %d lines) to clipboard.\n", label, chars, lines)
	case terminalErr == nil:
		fmt.Fprintf(w, "Sent %s (%d chars, %d lines) to terminal clipboard (OSC 52).\n", label, chars, lines)
	default:
		fmt.Fprintf(w, "Cannot copy %s: %v\n", label, terminalErr)
	}
}

func handleCopyCommand(parts []string, messages *[]db.Message, w io.Writer) {
	if messages == nil {
		fmt.Fprintln(w, "No conversation history found to copy.")
		return
	}

	args := parts[1:]
	if len(args) == 0 {
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
		deliverClipboardCopy(w, content, "last assistant response")
		return
	}

	first := strings.ToLower(args[0])
	if first == "help" || first == "-h" || first == "--help" {
		fmt.Fprintln(w, "usage: /copy [code | <n> | code <n> | list]")
		return
	}

	listMode := first == "list" || (first == "code" && len(args) == 2 && strings.EqualFold(args[1], "list"))
	if listMode {
		blocks := lastAssistantCodeBlocks(*messages)
		if len(blocks) == 0 {
			fmt.Fprintln(w, "No fenced code blocks found in assistant responses.")
			return
		}
		fmt.Fprintf(w, "Code blocks in latest assistant response (%d):\n", len(blocks))
		for i, blk := range blocks {
			lang := blk.Lang
			if lang == "" {
				lang = "text"
			}
			fmt.Fprintf(w, "  [%d] %s (%d lines): %s\n", i+1, lang, countLines(blk.Code), previewFirstLine(blk.Code))
		}
		fmt.Fprintln(w, "Run /copy <n> to copy a specific block, or /copy code for the last block.")
		return
	}

	var blockIndex int
	wantLastCode := false
	switch {
	case first == "code" && len(args) == 1:
		wantLastCode = true
	case first == "code" && len(args) == 2:
		n, err := strconv.Atoi(args[1])
		if err != nil || n <= 0 {
			fmt.Fprintln(w, "usage: /copy [code | <n> | code <n> | list]")
			return
		}
		blockIndex = n
	case len(args) == 1:
		n, err := strconv.Atoi(first)
		if err != nil || n <= 0 {
			fmt.Fprintln(w, "usage: /copy [code | <n> | code <n> | list]")
			return
		}
		blockIndex = n
	default:
		fmt.Fprintln(w, "usage: /copy [code | <n> | code <n> | list]")
		return
	}

	blocks := lastAssistantCodeBlocks(*messages)
	if len(blocks) == 0 {
		fmt.Fprintln(w, "No fenced code blocks found in assistant responses.")
		return
	}
	if wantLastCode {
		blockIndex = len(blocks)
	}
	if blockIndex > len(blocks) {
		fmt.Fprintf(w, "Code block #%d not found (only %d available; run /copy list).\n", blockIndex, len(blocks))
		return
	}
	blk := blocks[blockIndex-1]
	label := fmt.Sprintf("code block #%d", blockIndex)
	if blk.Lang != "" {
		label = fmt.Sprintf("code block #%d (%s)", blockIndex, blk.Lang)
	}
	deliverClipboardCopy(w, blk.Code, label)
}
