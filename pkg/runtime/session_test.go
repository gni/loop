package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"loop/pkg/agent"
	"loop/pkg/config"
)

func TestSessionRoundTripAndEphemeralRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	cfg := config.DefaultConfig()
	cfg.Endpoint = server.URL
	cfg.DisableLocalPlugins = true
	cfg.ApiKey = ""
	cfg.ShowThinking = false
	directory := t.TempDir()
	options := Options{Config: cfg, ConfigPath: filepath.Join(directory, "config.json"), Workspace: directory, NoSession: true, HTTPClient: server.Client(), Tools: []string{}}
	s, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Run(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != agent.RunCompleted || result.Text != "hello" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if s.State().Running {
		t.Fatal("run did not settle")
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "again"); err == nil {
		t.Fatal("closed session accepted work")
	}
}

func TestAbortAndQueueOrdering(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		once.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	defer server.Close()
	cfg := config.DefaultConfig()
	cfg.Endpoint = server.URL
	cfg.DisableLocalPlugins = true
	cfg.ApiKey = ""
	directory := t.TempDir()
	s, err := New(Options{Config: cfg, ConfigPath: filepath.Join(directory, "config.json"), Workspace: directory, NoSession: true, HTTPClient: server.Client(), Tools: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Run(context.Background(), "wait"); done <- err }()
	<-started
	if err := s.Queue("later", true); err != nil {
		t.Fatal(err)
	}
	if err := s.Queue("change direction", false); err != nil {
		t.Fatal(err)
	}
	if state := s.State(); len(state.Steering) != 1 || len(state.FollowUp) != 1 {
		t.Fatalf("queues mixed: %+v", state)
	}
	if err := s.Abort(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("aborted run succeeded")
	}
}

type queueProvider struct {
	started   chan struct{}
	release   chan struct{}
	calls     int
	histories [][]db.Message
}

func (p *queueProvider) CheckThinkingSupport(context.Context) bool { return false }
func (p *queueProvider) StreamChatCompletions(ctx context.Context, messages []db.Message, tools []tool.Tool, chunks chan<- agent.StreamChunk) (*db.Message, error) {
	p.calls++
	p.histories = append(p.histories, message.CloneAll(messages))
	if p.calls == 1 {
		close(p.started)
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &db.Message{Role: "assistant", ToolCalls: []db.ToolCall{{ID: "read-one", Type: "function", Function: db.ToolFunction{Name: "read", Arguments: `{"path":"input.txt"}`}}}}, nil
	}
	return &db.Message{Role: "assistant", Content: fmt.Sprintf("answer %d", p.calls)}, nil
}

func TestStartReservesWorkAndQueuesReachSafeTurns(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "input.txt"), []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.DisableLocalPlugins = true
	cfg.AutoApprove = true
	var s *Session
	settled := false
	var err error
	s, err = New(Options{Config: cfg, ConfigPath: filepath.Join(directory, "config.json"), Workspace: directory, NoSession: true, Tools: []string{"read"}, Events: func(event agent.Event) error {
		if event.Type == "agent_settled" {
			settled = true
			if s.State().Running {
				t.Error("settlement emitted while running")
			}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	provider := &queueProvider{started: make(chan struct{}), release: make(chan struct{})}
	s.Agent.LLMProvider = provider
	outcome, err := s.Start(context.Background(), "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(context.Background(), "duplicate"); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent prompt accepted: %v", err)
	}
	<-provider.started
	if err := s.Queue("change direction", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Queue("afterwards", true); err != nil {
		t.Fatal(err)
	}
	close(provider.release)
	result := <-outcome
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if !settled || provider.calls != 3 {
		t.Fatalf("unexpected settlement: %v %d", settled, provider.calls)
	}
	var prompts []string
	for _, entry := range s.State().Messages {
		if entry.Role == "user" {
			prompts = append(prompts, entry.Content)
		}
	}
	if strings.Join(prompts, "|") != "first|change direction|afterwards" {
		t.Fatalf("queue order: %v", prompts)
	}
	if last := provider.histories[1][len(provider.histories[1])-1]; last.Content != "change direction" {
		t.Fatalf("steering missed safe turn: %+v", last)
	}
	if _, err := os.Stat(filepath.Join(directory, "sessions")); !os.IsNotExist(err) {
		t.Fatal("ephemeral run created conversation storage")
	}
}

func TestResumeRestoresLatestSettingsAndPlan(t *testing.T) {
	directory := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DisableLocalPlugins = true
	cfg.ActiveProvider = "fixture"
	cfg.Model = "one"
	cfg.Providers = map[string]config.ProviderConfig{"fixture": {Endpoint: "http://127.0.0.1:1", Model: "one"}}
	options := Options{Config: cfg, ConfigPath: filepath.Join(directory, "config.json"), Workspace: directory}
	s, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := s.Configuration()
	if err != nil {
		t.Fatal(err)
	}
	changed.Model = "two"
	if err := s.UpdateConfig(changed); err != nil {
		t.Fatal(err)
	}
	if err := s.Agent.SetTodos([]tool.TodoItem{{ID: "step", Task: "finish", Status: "in_progress"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTools([]string{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	options.SessionID = s.ID
	resumed, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close(context.Background())
	if resumed.Agent.Config.Model != "two" || resumed.options.Tools == nil || len(resumed.Agent.GetTodos()) != 1 {
		t.Fatalf("resume lost state: %+v %+v", resumed.State(), resumed.Agent.GetTodos())
	}
}
