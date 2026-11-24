package commands

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"loop/pkg/db"
	"loop/pkg/ui"
)

func TestExtractFencedCodeBlocks(t *testing.T) {
	md := strings.Join([]string{
		"Here is some inline ```not a block``` text.",
		"",
		"```go title=\"main.go\"",
		"package main",
		"",
		"func main() {}",
		"```",
		"",
		"~~~bash",
		"echo \"hello\"",
		"~~~",
		"",
		"````markdown",
		"```txt",
		"nested fence",
		"```",
		"````",
	}, "\n")

	blocks := extractFencedCodeBlocks(md)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].Lang != "go" || blocks[0].Code != "package main\n\nfunc main() {}" {
		t.Fatalf("block 0 mismatch: %+v", blocks[0])
	}
	if blocks[1].Lang != "bash" || blocks[1].Code != "echo \"hello\"" {
		t.Fatalf("block 1 mismatch: %+v", blocks[1])
	}
	if blocks[2].Lang != "markdown" || blocks[2].Code != "```txt\nnested fence\n```" {
		t.Fatalf("block 2 mismatch: %+v", blocks[2])
	}
}

func TestHandleCopyCommandModesAndRawOSC52(t *testing.T) {
	// Ensure no external clipboard binary env vars interfere with deterministic OSC 52 assertions.
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("WSL_DISTRO_NAME", "")
	t.Setenv("WSL_INTEROP", "")
	t.Setenv("TMUX", "")
	t.Setenv("STY", "")

	reply := "Summary:\n\n```go\nfmt.Println(\"one\")\n```\n\nAnd shell:\n\n```sh\nls -la\n```"
	msgs := []db.Message{
		{Role: "user", Content: "show examples"},
		{Role: "assistant", Content: reply},
	}

	assertOSC52 := func(t *testing.T, raw, wantPayload string) {
		t.Helper()
		wantSeq := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(wantPayload)) + "\x07"
		if !strings.Contains(raw, wantSeq) {
			t.Fatalf("expected raw writer to contain OSC 52 for %q, got %q", wantPayload, raw)
		}
	}

	t.Run("/copy full response writes raw OSC 52 via unwrapped writer", func(t *testing.T) {
		var rawBuf bytes.Buffer
		pp := ui.NewPromptPreservingWriter(&rawBuf, 24)
		pp.SetNativeScroll(true)

		handleCopyCommand([]string{"/copy"}, &msgs, pp)
		assertOSC52(t, rawBuf.String(), reply)
		if !strings.Contains(rawBuf.String(), "last assistant response") {
			t.Fatalf("expected confirmation message, got %q", rawBuf.String())
		}
	})

	t.Run("/copy code copies last fenced code block", func(t *testing.T) {
		var buf bytes.Buffer
		handleCopyCommand([]string{"/copy", "code"}, &msgs, &buf)
		assertOSC52(t, buf.String(), "ls -la")
		if !strings.Contains(buf.String(), "code block #2 (sh)") {
			t.Fatalf("expected block #2 (sh) confirmation, got %q", buf.String())
		}
	})

	t.Run("/copy 1 and /copy code 1 copy first fenced code block", func(t *testing.T) {
		for _, parts := range [][]string{{"/copy", "1"}, {"/copy", "code", "1"}} {
			var buf bytes.Buffer
			handleCopyCommand(parts, &msgs, &buf)
			assertOSC52(t, buf.String(), `fmt.Println("one")`)
			if !strings.Contains(buf.String(), "code block #1 (go)") {
				t.Fatalf("parts=%v: expected block #1 (go) confirmation, got %q", parts, buf.String())
			}
		}
	})

	t.Run("/copy list lists all fenced code blocks", func(t *testing.T) {
		var buf bytes.Buffer
		handleCopyCommand([]string{"/copy", "list"}, &msgs, &buf)
		out := buf.String()
		if !strings.Contains(out, "[1] go (1 lines)") || !strings.Contains(out, "[2] sh (1 lines)") {
			t.Fatalf("unexpected /copy list output: %q", out)
		}
		if strings.Contains(out, "\x1b]52;") {
			t.Fatalf("/copy list should not emit OSC 52: %q", out)
		}
	})

	t.Run("out of range and empty history", func(t *testing.T) {
		var buf bytes.Buffer
		handleCopyCommand([]string{"/copy", "9"}, &msgs, &buf)
		if !strings.Contains(buf.String(), "not found") {
			t.Fatalf("expected out-of-range error, got %q", buf.String())
		}

		buf.Reset()
		var empty []db.Message
		handleCopyCommand([]string{"/copy"}, &empty, &buf)
		if !strings.Contains(buf.String(), "No assistant response") {
			t.Fatalf("expected empty history message, got %q", buf.String())
		}
	})
}

func TestIsBuiltinSlashCommand(t *testing.T) {
	yes := []string{
		"/config native_scroll true",
		"  /copy code  ",
		"/help",
		"/resume abc-123",
		"/set native_scroll true",
		"/exit",
	}
	for _, s := range yes {
		if !IsBuiltinSlashCommand(s) {
			t.Fatalf("expected IsBuiltinSlashCommand(%q) == true", s)
		}
	}
	no := []string{
		"",
		"hello",
		"/usr/local/bin/loop",
		"/tmp/example.txt",
		"/config native_scroll true\nextra line",
		"/unknown_cmd foo",
	}
	for _, s := range no {
		if IsBuiltinSlashCommand(s) {
			t.Fatalf("expected IsBuiltinSlashCommand(%q) == false", s)
		}
	}
}
