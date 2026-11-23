package agent

import (
	"bytes"
	"context"
	"io"
	"sync"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

// sessionHooks wires runtime session orchestration and external callbacks into Agent.
type sessionHooks struct {
	ReloadResources     func() error
	TransformContext    func(context.Context, []db.Message) ([]db.Message, error)
	RunSession          func(context.Context, io.Writer, *[]db.Message, string, []string, style.UITheme, string) (RunResult, error)
	SyncSessionState    func([]db.Message, []string, string, bool) error
	SelectSessionBranch func(context.Context, string) ([]db.Message, error)
	SessionMessages     func() []db.Message
	NextInput           func() []db.Message
	Inquirer            func(string, []tool.AskUserOption, string) (string, error)
	EventSink           func(Event) error
	DisposeAgents       func()
	ClearAgentsFunc     func() error
}

// persistenceState records session storage write failures across a turn.
type persistenceState struct {
	persistenceErr      error
	persistenceFirstErr error
	persistenceFailures uint64
}

// streamPresentationState holds active-turn rendering and replay hooks.
type streamPresentationState struct {
	CurrentStreamBuffer *bytes.Buffer
	// CurrentHistorySnapshot supplies the history owned by the running loop.
	// Invoke on the foreground presentation goroutine, alongside stream replay.
	CurrentHistorySnapshot func() []db.Message
	// CurrentStreamReplay renders semantic, not terminal-byte, output from
	// the uncommitted model step. Its renderer locks its own snapshot.
	CurrentStreamReplay func(io.Writer, bool, style.UITheme)
	CurrentStreamMu     sync.Mutex
	// OnStreamProgress runs on the foreground stream consumer after a chunk
	// has been rendered. It may request a safe redraw of the active turn.
	OnStreamProgress func() bool
	TurnStartTime    time.Time
	CurrentLoader    TurnLoader
}

// turnRuntimeState owns per-session and per-turn mutable execution state under stateMu.
type turnRuntimeState struct {
	stateMu                 sync.Mutex
	activeSessionID         string
	sessionGeneration       uint64
	sessionAutoApprove      bool
	sessionName             string
	sessionNameKnown        bool
	sessionNameDirty        bool
	liveToolWriter          io.Writer
	currentWriter           io.Writer
	currentContext          context.Context
	currentTheme            style.UITheme
	lastToolOutput          string
	lastToolIsError         bool
	lastToolWasEdit         bool
	lastGenerationDuration  time.Duration
	liveBodyStreamed        bool
	droppedChunks           int64
	lastTurnDropped         int64
	emittedMemoryErrors     map[string]bool
	ForceSystemPromptUpdate bool
}

// coordinationState tracks swarm membership, system events, and task todos.
type coordinationState struct {
	SpawnedAgents      map[string]bool
	SpawnedAgentsMu    sync.RWMutex
	SystemEvents       chan string
	pendingSystemEvent string
	MultiAgentManager  any
	Todos              []tool.TodoItem
	TodosMu            sync.RWMutex
}

// TurnStatusPresenter is an optional AgentUI capability that styles turn footer
// and compaction status notices in interactive terminals while keeping headless
// runs free of ANSI formatting noise.
type TurnStatusPresenter interface {
	FormatTurnElapsed(startTime time.Time, theme style.UITheme, showMicroseconds bool) string
	FormatCompletionTokensBadge(tokens int, theme style.UITheme) string
	FormatSystemNotice(text string, level string, theme style.UITheme) string
}
