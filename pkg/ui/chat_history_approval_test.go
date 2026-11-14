package ui

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/ui/interactive"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func approvalHistory(t *testing.T) (*ChatHistory, *bytes.Buffer) {
	t.Helper()
	state := getUI()
	state.StateMu.Lock()
	beforeApproval, beforeMenu := state.InApprovalPrompt, state.InInteractiveMenu
	state.InApprovalPrompt, state.InInteractiveMenu = true, false
	state.StateMu.Unlock()
	t.Cleanup(func() {
		state.StateMu.Lock()
		state.InApprovalPrompt, state.InInteractiveMenu = beforeApproval, beforeMenu
		state.StateMu.Unlock()
	})
	var output bytes.Buffer
	width, height := GetTerminalSize()
	reader := &interceptor.KeyInterceptorReader{W: &output}
	h := &ChatHistory{
		a: &agent.Agent{Config: config.DefaultConfig()}, reader: reader,
		writer: NewPromptPreservingWriter(&output, height), searchRow: -1,
		buffer: render.NewScrollback(width, style.ConsoleScrollBottom(height, 0)),
	}
	h.writer.inner = &historyOutput{destination: &output, history: h}
	t.Cleanup(h.Close)
	for i := range 100 {
		fmt.Fprintf(h.buffer, "history line %d\n", i)
	}
	fmt.Fprint(h.buffer, "approve tool execution?\nyes, proceed\nno, cancel\nalways allow in this session")
	h.top = h.buffer.Base()
	return h, &output
}

func TestApprovalHistoryNavigation(t *testing.T) {
	// UI and terminal state are shared; these cases must run serially.
	for _, key := range []string{"\x1b[5~", "\x1b[<64;10;5M", "\x1b[M`*%"} {
		t.Run(fmt.Sprintf("%q", key), func(t *testing.T) {
			h, output := approvalHistory(t)
			if got := h.FilterInput([]byte(key)); len(got) != 0 {
				t.Fatalf("history navigation reached approval: %q", got)
			}
			TerminalMu.Lock()
			browsing := h.browsing
			TerminalMu.Unlock()
			if !browsing {
				t.Fatal("approval prevented history scrolling")
			}
			deadline := time.Now().Add(time.Second)
			for {
				TerminalMu.Lock()
				painted := strings.Contains(output.String(), "history line")
				TerminalMu.Unlock()
				if painted {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("history viewport did not repaint during approval")
				}
				time.Sleep(time.Millisecond)
			}
			if got := h.FilterInput([]byte("\x1b[F")); len(got) != 0 {
				t.Fatalf("End reached approval: %q", got)
			}
			TerminalMu.Lock()
			defer TerminalMu.Unlock()
			if h.browsing {
				t.Fatal("End did not restore approval choices")
			}
			if !strings.Contains(output.String(), "approve tool execution?") {
				t.Fatal("approval choices were not restored")
			}
		})
	}
}

func TestApprovalChoiceInputRestoresLatest(t *testing.T) {
	for _, key := range []string{"\x1b[B", "\r", "n", "\x1b", "\x1b[200~"} {
		t.Run(fmt.Sprintf("%q", key), func(t *testing.T) {
			h, _ := approvalHistory(t)
			TerminalMu.Lock()
			h.top = max(0, h.buffer.Base()-3)
			h.browsing = true
			TerminalMu.Unlock()
			if got := h.FilterInput([]byte(key)); string(got) != key {
				t.Fatalf("choice input changed: got %q want %q", got, key)
			}
			TerminalMu.Lock()
			defer TerminalMu.Unlock()
			if h.browsing {
				t.Fatal("choice input left the approval menu offscreen")
			}
		})
	}
}

func TestFullscreenMenuStillOwnsHistoryInput(t *testing.T) {
	h, _ := approvalHistory(t)
	getUI().StateMu.Lock()
	getUI().InInteractiveMenu = true
	getUI().StateMu.Unlock()
	key := "\x1b[5~"
	if got := h.FilterInput([]byte(key)); string(got) != key {
		t.Fatalf("full-screen menu input intercepted: %q", got)
	}
}

func TestApprovalMenuRemainsPendingWhileBrowsing(t *testing.T) {
	h, output := approvalHistory(t)
	input := make(chan byte, 32)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	state := getUI()
	state.StateMu.Lock()
	previous := state.ActiveInputReader
	state.ActiveInputReader = &interceptor.ApprovalByteReader{Context: ctx, Input: input}
	state.StateMu.Unlock()
	result := make(chan [2]bool, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		allowed, session := interactive.AskForApproval(h.writer, style.GetTheme("plain"), true)
		result <- [2]bool{allowed, session}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		state.StateMu.Lock()
		state.ActiveInputReader = previous
		state.StateMu.Unlock()
	})
	deadline := time.Now().Add(time.Second)
	for {
		TerminalMu.Lock()
		rendered := strings.Contains(output.String(), "yes, proceed")
		TerminalMu.Unlock()
		if rendered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("approval menu did not render")
		}
		time.Sleep(time.Millisecond)
	}
	if got := h.FilterInput([]byte("\x1b[5~")); len(got) != 0 {
		t.Fatalf("PageUp escaped history filter: %q", got)
	}
	TerminalMu.Lock()
	browsing := h.browsing
	TerminalMu.Unlock()
	if !browsing {
		t.Fatal("could not browse history with the real approval menu open")
	}
	select {
	case <-result:
		t.Fatal("scrolling answered the approval")
	default:
	}
	// Down still chooses 'no'; Enter confirms it after restoring the menu.
	for _, key := range []string{"\x1b[B", "\r"} {
		for _, b := range h.FilterInput([]byte(key)) {
			input <- b
		}
	}
	select {
	case got := <-result:
		if got != [2]bool{false, false} {
			t.Fatalf("Down+Enter changed approval semantics: %v", got)
		}
	case <-ctx.Done():
		t.Fatal("approval did not complete")
	}
}
