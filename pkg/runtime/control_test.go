package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"loop/pkg/config"
)

func TestClosedSessionRejectsMutations(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Options{Config: &config.Config{Model: "original"}, ConfigPath: filepath.Join(dir, "config.json"), Workspace: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range sessionMutations(s) {
		t.Run(name, func(t *testing.T) {
			if err := mutate(); err == nil || !strings.Contains(err.Error(), "session is closed") {
				t.Fatalf("expected closed error, got %v", err)
			}
		})
	}
	if s.Agent.Config.Model != "original" {
		t.Fatal("closed session config changed")
	}
}

func TestRunningSessionRejectsMutations(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Options{Config: &config.Config{}, ConfigPath: filepath.Join(dir, "config.json"), Workspace: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	s.runMu.Lock()
	defer s.runMu.Unlock()
	for name, mutate := range sessionMutations(s) {
		t.Run(name, func(t *testing.T) {
			if err := mutate(); !errors.Is(err, ErrBusy) {
				t.Fatalf("expected busy error, got %v", err)
			}
		})
	}
}

func sessionMutations(s *Session) map[string]func() error {
	return map[string]func() error{
		"configuration":    func() error { return s.UpdateConfig(&config.Config{Model: "changed"}) },
		"tools":            func() error { return s.SetTools([]string{"read"}) },
		"compaction":       func() error { return s.Compact(context.Background()) },
		"reload":           s.Reload,
		"extension":        func() error { _, err := s.ExecuteCommand(context.Background(), "example", nil); return err },
		"interactive sync": func() error { return s.SyncInteractive(nil, nil, "other", false) },
	}
}

func TestToolSelectionDoesNotAliasCallerSlice(t *testing.T) {
	dir := t.TempDir()
	tools := []string{"read"}
	s, err := New(Options{Config: &config.Config{}, ConfigPath: filepath.Join(dir, "config.json"), Workspace: dir, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	tools[0] = "bash"
	if s.Agent.ToolSelection[0] != "read" {
		t.Fatal("constructor retained caller's tool slice")
	}
	tools = []string{"grep"}
	if err := s.SyncInteractive(nil, tools, s.ID, false); err != nil {
		t.Fatal(err)
	}
	tools[0] = "bash"
	if s.Agent.ToolSelection[0] != "grep" {
		t.Fatal("interactive sync retained caller's tool slice")
	}
}

func TestQueueRejectsWhitespaceOnlyPrompt(t *testing.T) {
	s := &Session{}
	if err := s.Queue(" \t\n ", false); err == nil {
		t.Fatal("blank prompt accepted")
	}
}
