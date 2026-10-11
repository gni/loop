// Package limits holds the operational ceilings that were previously hardcoded
// as package-level consts (search caps, guard streak bounds, task registry size,
// skill size caps, token budgets). Each value keeps a compiled default so the
// binary behaves identically when no config is supplied, but a config file can
// raise or lower it. Values are clamped on the way in: a config file is untrusted
// input, so a limit can never be pushed past its safety ceiling or below its
// minimum.
package limits

import "time"

// Safety ceilings. These are NOT configurable: they bound whatever a config file
// is allowed to request, so a misconfigured or hostile config cannot disable a
// guard or turn a tool into an unbounded scanner.
const (
	AbsoluteMaxGrepLimit         = 1000
	AbsoluteMaxGrepOutputBytes   = 512 * 1024
	AbsoluteMaxGrepFileBytes     = 16 * 1024 * 1024
	AbsoluteMaxLineLength        = 4096
	AbsoluteMaxWindowsPerTarget  = 512
	AbsoluteMaxTrackedTasks      = 4096
	AbsoluteMaxTaskRetention     = 30 * time.Minute
	AbsoluteMaxNameLength        = 128
	AbsoluteMaxSkillDescBytes    = 8192
	AbsoluteMaxSkillContentBytes = 512 * 1024
	AbsoluteMaxCompletionFloor   = 4096

	// Truncation ceilings: a config file may lower a tool's output budget but can
	// never turn it into an unbounded scanner or an unbounded read.
	AbsoluteMaxToolOutputLines  = 4000
	AbsoluteMaxToolOutputBytes  = 512 * 1024
	AbsoluteMaxReadFileBytes    = 1024 * 1024
	AbsoluteMaxFindResults      = 2000
	AbsoluteMaxListEntries      = 1000
	AbsoluteMaxListDepth        = 8
	AbsoluteMaxParallelWorkers  = 16
	AbsoluteMaxCompactionTokens = 65536
	AbsoluteMaxMCPServerWorkers = 16
	// A connect timeout above this would make first paint slower than the value it is
	// meant to protect against; the point of the ceiling is that startup never waits
	// more than a bounded time per server.
	AbsoluteMaxMCPConnectTimeout      = 30 * time.Second
	AbsoluteMaxMCPRequestTimeout      = 60 * time.Second
	AbsoluteMaxEscapeDisambiguation   = 100 * time.Millisecond
	AbsoluteMaxSubagentDepth     = 16
	// AbsoluteMinConsecutiveLimit keeps the repetition guard switchable-on but
	// never disabled: a guard threshold of 1 or 0 would block every call or
	// silently turn the guard off.
	AbsoluteMinConsecutiveLimit = 2
	AbsoluteMaxConsecutiveLimit = 100
	AbsoluteMaxReasoningBudget  = 32768
)

// Defaults are the historical hardcoded values.
const (
	DefaultGrepDefaultLimit     = 100
	DefaultGrepMaxLimit         = 500
	DefaultGrepLineLength       = 500
	DefaultGrepOutputBytes      = 64 * 1024
	DefaultGrepFileBytes        = 2 * 1024 * 1024
	DefaultConsecutiveLimit     = 10
	DefaultWindowsPerTarget     = 64
	DefaultMaxTrackedTasks      = 256
	DefaultTaskRetentionGrace   = time.Minute
	DefaultAuditTruncationChars = 1500
	DefaultNameMaxLength        = 64
	DefaultSkillDescBytes       = 2048
	DefaultSkillContentBytes    = 128 * 1024
	DefaultCompletionTokenFloor = 512
	DefaultMaxSubagentDepth     = 4
	DefaultSupervisedTasks      = 512
	DefaultMaxCompletionTokens  = 16384

	// Truncation defaults: two independent limits, first hit wins. The per-line
	// cap is the grep match cap; the read line cap is the
	// historical read per-line cap.
	DefaultToolOutputLines   = 2000
	DefaultToolOutputBytes   = 50 * 1024
	DefaultReadFileBytes     = 500 * 1024
	DefaultReadLineLength    = 1000
	DefaultFindLimit         = 500
	DefaultListEntries       = 150
	DefaultListDepth         = 3
	DefaultParallelWorkers   = 4
	DefaultCompactionReserve = 16384

	// MCP startup defaults. ConnectTimeout bounds the wait for the SSE endpoint event
	// and for the response headers; RequestTimeout bounds a single JSON-RPC call.
	// ConnectTimeout is deliberately far below the historical 5s endpoint wait so a
	// server that never emits an endpoint event fails fast instead of stalling first
	// paint.
	DefaultMCPServerWorkers     = 4
	DefaultMCPConnectTimeout    = 2 * time.Second
	DefaultMCPRequestTimeout    = 15 * time.Second

	// EscapeDisambiguation is how long a lone ESC byte is held to see whether it begins
	// a sequence. The buffered channel is checked without waiting first, so this only
	// applies when the next byte has not arrived; a lone escape keypress pays it in
	// full, which is why the window is kept short.
	DefaultEscapeDisambiguation = 10 * time.Millisecond
	DefaultCompactionKeepRecent = 20000
)

// Limits is the configurable subset. Zero means "use the default".
type Limits struct {
	GrepDefaultLimit           int           `json:"grep_default_limit,omitempty"`
	GrepMaxLimit               int           `json:"grep_max_limit,omitempty"`
	GrepMaxLineLength          int           `json:"grep_max_line_length,omitempty"`
	GrepMaxOutputBytes         int           `json:"grep_max_output_bytes,omitempty"`
	GrepMaxFileBytes           int           `json:"grep_max_file_bytes,omitempty"`
	WindowsPerTarget           int           `json:"max_windows_per_target,omitempty"`
	MaxTrackedTasks            int           `json:"max_tracked_tasks,omitempty"`
	MaxSupervisedTasks         int           `json:"max_supervised_tasks,omitempty"`
	TaskRetentionGrace         time.Duration `json:"task_retention_grace,omitempty"`
	AuditTruncationChars       int           `json:"audit_truncation_chars,omitempty"`
	NameMaxLength              int           `json:"agent_name_max_chars,omitempty"`
	SkillDescMaxBytes          int           `json:"skill_desc_max_bytes,omitempty"`
	SkillContentMaxBytes       int           `json:"skill_content_max_bytes,omitempty"`
	CompletionTokenFloor       int           `json:"max_completion_tokens_floor,omitempty"`
	MaxSubagentDepth           int           `json:"max_subagent_depth,omitempty"`
	ConsecutiveLimit           int           `json:"repeat_guard_limit,omitempty"`
	MaxOutputLines             int           `json:"max_output_lines,omitempty"`
	MaxOutputBytes             int           `json:"max_output_bytes,omitempty"`
	MaxReadFileBytes           int           `json:"max_read_file_bytes,omitempty"`
	ReadMaxLineLength          int           `json:"read_max_line_length,omitempty"`
	FindMaxResults             int           `json:"find_max_results,omitempty"`
	ListMaxEntries             int           `json:"list_max_entries,omitempty"`
	ListMaxDepth               int           `json:"list_max_depth,omitempty"`
	ParallelToolWorkers        int           `json:"parallel_tool_workers,omitempty"`
	CompactionReserveTokens    int           `json:"compaction_reserve_tokens,omitempty"`
	MCPServerWorkers           int           `json:"mcp_server_workers,omitempty"`
	MCPConnectTimeout          time.Duration `json:"mcp_connect_timeout,omitempty"`
	MCPRequestTimeout          time.Duration `json:"mcp_request_timeout,omitempty"`
	EscapeDisambiguation       time.Duration `json:"escape_disambiguation,omitempty"`
	CompactionKeepRecentTokens int           `json:"compaction_keep_recent_tokens,omitempty"`
	// ReasoningTokenBudgets maps a reasoning effort level to its thinking token
	// budget. A missing or zero key falls back to the compiled default; -1 means
	// "no budget" (unbounded) and is not clamped.
	ReasoningTokenBudgets map[string]int `json:"reasoning_token_budgets,omitempty"`
}

// DefaultReasoningBudgets returns the compiled per-effort thinking budgets.
func DefaultReasoningBudgets() map[string]int {
	return map[string]int{"low": 512, "medium": 2048, "high": 8192, "max": -1}
}

// Defaults returns the compiled defaults.
func Defaults() Limits {
	return Limits{
		GrepDefaultLimit:           DefaultGrepDefaultLimit,
		GrepMaxLimit:               DefaultGrepMaxLimit,
		GrepMaxLineLength:          DefaultGrepLineLength,
		GrepMaxOutputBytes:         DefaultGrepOutputBytes,
		GrepMaxFileBytes:           DefaultGrepFileBytes,
		WindowsPerTarget:           DefaultWindowsPerTarget,
		MaxTrackedTasks:            DefaultMaxTrackedTasks,
		MaxSupervisedTasks:         DefaultSupervisedTasks,
		TaskRetentionGrace:         DefaultTaskRetentionGrace,
		AuditTruncationChars:       DefaultAuditTruncationChars,
		NameMaxLength:              DefaultNameMaxLength,
		SkillDescMaxBytes:          DefaultSkillDescBytes,
		SkillContentMaxBytes:       DefaultSkillContentBytes,
		CompletionTokenFloor:       DefaultCompletionTokenFloor,
		MaxSubagentDepth:           DefaultMaxSubagentDepth,
		ConsecutiveLimit:           DefaultConsecutiveLimit,
		MaxOutputLines:             DefaultToolOutputLines,
		MaxOutputBytes:             DefaultToolOutputBytes,
		MaxReadFileBytes:           DefaultReadFileBytes,
		ReadMaxLineLength:          DefaultReadLineLength,
		FindMaxResults:             DefaultFindLimit,
		ListMaxEntries:             DefaultListEntries,
		ListMaxDepth:               DefaultListDepth,
		ParallelToolWorkers:        DefaultParallelWorkers,
		CompactionReserveTokens:    DefaultCompactionReserve,
		CompactionKeepRecentTokens: DefaultCompactionKeepRecent,
		MCPServerWorkers:           DefaultMCPServerWorkers,
		MCPConnectTimeout:          DefaultMCPConnectTimeout,
		MCPRequestTimeout:          DefaultMCPRequestTimeout,
		EscapeDisambiguation:       DefaultEscapeDisambiguation,
		ReasoningTokenBudgets:      DefaultReasoningBudgets(),
	}
}

// Resolve layers non-zero overrides over defaults and clamps every value to its
// safety ceiling. A value below 1 is ignored (default wins), which also keeps
// guards from being turned off entirely.
func Resolve(overrides Limits) Limits {
	out := Defaults()
	if overrides.GrepDefaultLimit > 0 {
		out.GrepDefaultLimit = clamp(overrides.GrepDefaultLimit, 1, AbsoluteMaxGrepLimit)
	}
	if overrides.GrepMaxLimit > 0 {
		out.GrepMaxLimit = clamp(overrides.GrepMaxLimit, 1, AbsoluteMaxGrepLimit)
	}
	// A default above the max is meaningless: lower the default rather than
	// silently raising the requested max.
	if out.GrepDefaultLimit > out.GrepMaxLimit {
		out.GrepDefaultLimit = out.GrepMaxLimit
	}
	if overrides.GrepMaxLineLength > 0 {
		out.GrepMaxLineLength = clamp(overrides.GrepMaxLineLength, 1, AbsoluteMaxLineLength)
	}
	if overrides.GrepMaxOutputBytes > 0 {
		out.GrepMaxOutputBytes = clamp(overrides.GrepMaxOutputBytes, 1, AbsoluteMaxGrepOutputBytes)
	}
	if overrides.GrepMaxFileBytes > 0 {
		out.GrepMaxFileBytes = clamp(overrides.GrepMaxFileBytes, 1, AbsoluteMaxGrepFileBytes)
	}
	if overrides.WindowsPerTarget > 0 {
		out.WindowsPerTarget = clamp(overrides.WindowsPerTarget, 1, AbsoluteMaxWindowsPerTarget)
	}
	if overrides.MaxTrackedTasks > 0 {
		out.MaxTrackedTasks = clamp(overrides.MaxTrackedTasks, 1, AbsoluteMaxTrackedTasks)
	}
	if overrides.MaxSupervisedTasks > 0 {
		out.MaxSupervisedTasks = clamp(overrides.MaxSupervisedTasks, 1, AbsoluteMaxTrackedTasks)
	}
	if overrides.TaskRetentionGrace > 0 {
		out.TaskRetentionGrace = clampDuration(overrides.TaskRetentionGrace, DefaultTaskRetentionGrace, AbsoluteMaxTaskRetention)
	}
	if overrides.AuditTruncationChars > 0 {
		out.AuditTruncationChars = overrides.AuditTruncationChars
	}
	if overrides.NameMaxLength > 0 {
		out.NameMaxLength = clamp(overrides.NameMaxLength, 1, AbsoluteMaxNameLength)
	}
	if overrides.SkillDescMaxBytes > 0 {
		out.SkillDescMaxBytes = clamp(overrides.SkillDescMaxBytes, 1, AbsoluteMaxSkillDescBytes)
	}
	if overrides.SkillContentMaxBytes > 0 {
		out.SkillContentMaxBytes = clamp(overrides.SkillContentMaxBytes, 1, AbsoluteMaxSkillContentBytes)
	}
	if overrides.CompletionTokenFloor > 0 {
		out.CompletionTokenFloor = clamp(overrides.CompletionTokenFloor, 1, AbsoluteMaxCompletionFloor)
	}
	if len(out.ReasoningTokenBudgets) == 0 {
		out.ReasoningTokenBudgets = DefaultReasoningBudgets()
	}
	for k, v := range overrides.ReasoningTokenBudgets {
		if v == 0 {
			continue
		}
		if v > 0 {
			v = clamp(v, 1, AbsoluteMaxReasoningBudget)
		}
		out.ReasoningTokenBudgets[k] = v
	}
	if overrides.ConsecutiveLimit > 0 {
		out.ConsecutiveLimit = clamp(overrides.ConsecutiveLimit, AbsoluteMinConsecutiveLimit, AbsoluteMaxConsecutiveLimit)
	}
	if overrides.MaxSubagentDepth > 0 {
		out.MaxSubagentDepth = clamp(overrides.MaxSubagentDepth, 0, AbsoluteMaxSubagentDepth)
	}
	if overrides.MaxOutputLines > 0 {
		out.MaxOutputLines = clamp(overrides.MaxOutputLines, 1, AbsoluteMaxToolOutputLines)
	}
	if overrides.MaxOutputBytes > 0 {
		out.MaxOutputBytes = clamp(overrides.MaxOutputBytes, 1, AbsoluteMaxToolOutputBytes)
	}
	if overrides.MaxReadFileBytes > 0 {
		out.MaxReadFileBytes = clamp(overrides.MaxReadFileBytes, 1, AbsoluteMaxReadFileBytes)
	}
	if overrides.ReadMaxLineLength > 0 {
		out.ReadMaxLineLength = clamp(overrides.ReadMaxLineLength, 1, AbsoluteMaxLineLength)
	}
	if overrides.FindMaxResults > 0 {
		out.FindMaxResults = clamp(overrides.FindMaxResults, 1, AbsoluteMaxFindResults)
	}
	if overrides.ListMaxEntries > 0 {
		out.ListMaxEntries = clamp(overrides.ListMaxEntries, 1, AbsoluteMaxListEntries)
	}
	if overrides.ListMaxDepth > 0 {
		out.ListMaxDepth = clamp(overrides.ListMaxDepth, 1, AbsoluteMaxListDepth)
	}
	if overrides.ParallelToolWorkers > 0 {
		out.ParallelToolWorkers = clamp(overrides.ParallelToolWorkers, 1, AbsoluteMaxParallelWorkers)
	}
	if overrides.CompactionReserveTokens > 0 {
		out.CompactionReserveTokens = clamp(overrides.CompactionReserveTokens, 1, AbsoluteMaxCompactionTokens)
	}
	if overrides.CompactionKeepRecentTokens > 0 {
		out.CompactionKeepRecentTokens = clamp(overrides.CompactionKeepRecentTokens, 1, AbsoluteMaxCompactionTokens)
	}
	if overrides.MCPServerWorkers > 0 {
		out.MCPServerWorkers = clamp(overrides.MCPServerWorkers, 1, AbsoluteMaxMCPServerWorkers)
	}
	if overrides.MCPConnectTimeout > 0 {
		out.MCPConnectTimeout = clampDuration(overrides.MCPConnectTimeout, 100*time.Millisecond, AbsoluteMaxMCPConnectTimeout)
	}
	if overrides.MCPRequestTimeout > 0 {
		out.MCPRequestTimeout = clampDuration(overrides.MCPRequestTimeout, 1*time.Second, AbsoluteMaxMCPRequestTimeout)
	}
	if overrides.EscapeDisambiguation > 0 {
		out.EscapeDisambiguation = clampDuration(overrides.EscapeDisambiguation, time.Millisecond, AbsoluteMaxEscapeDisambiguation)
	}
	return out
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func clampDuration(d, min, max time.Duration) time.Duration {
	if d < min {
		return min
	}
	if d > max {
		return max
	}
	return d
}

// Master is the live, resolved set. It starts at the compiled defaults and is
// replaced once at startup by the config loader, mirroring the prompt catalog
// pattern so call sites need no config plumbing.
var Master = Defaults()

// Set installs a resolved set as the live limits.
func Set(l Limits) { Master = Resolve(l) }

// Accessors used by call sites; each returns the live value.
func GrepDefaultLimit() int             { return Master.GrepDefaultLimit }
func GrepMaxLimit() int                 { return Master.GrepMaxLimit }
func GrepMaxLineLength() int            { return Master.GrepMaxLineLength }
func GrepMaxOutputBytes() int           { return Master.GrepMaxOutputBytes }
func GrepMaxFileBytes() int             { return Master.GrepMaxFileBytes }
func WindowsPerTarget() int             { return Master.WindowsPerTarget }
func MaxTrackedTasks() int              { return Master.MaxTrackedTasks }
func MaxSupervisedTasks() int           { return Master.MaxSupervisedTasks }
func TaskRetentionGrace() time.Duration { return Master.TaskRetentionGrace }
func AuditTruncationChars() int         { return Master.AuditTruncationChars }
func NameMaxLength() int                { return Master.NameMaxLength }
func SkillDescMaxBytes() int            { return Master.SkillDescMaxBytes }
func SkillContentMaxBytes() int         { return Master.SkillContentMaxBytes }
func CompletionTokenFloor() int         { return Master.CompletionTokenFloor }
func SubagentDepth() int                { return Master.MaxSubagentDepth }
func ConsecutiveLimit() int             { return Master.ConsecutiveLimit }
func MaxOutputLines() int               { return Master.MaxOutputLines }
func MaxOutputBytes() int               { return Master.MaxOutputBytes }
func MaxReadFileBytes() int             { return Master.MaxReadFileBytes }
func ReadMaxLineLength() int            { return Master.ReadMaxLineLength }
func FindMaxResults() int               { return Master.FindMaxResults }
func ListMaxEntries() int               { return Master.ListMaxEntries }
func ListMaxDepth() int                 { return Master.ListMaxDepth }
func ParallelToolWorkers() int          { return Master.ParallelToolWorkers }
func CompactionReserveTokens() int      { return Master.CompactionReserveTokens }
func CompactionKeepRecentTokens() int   { return Master.CompactionKeepRecentTokens }
func MCPServerWorkers() int             { return Master.MCPServerWorkers }
func MCPConnectTimeout() time.Duration  { return Master.MCPConnectTimeout }
func MCPRequestTimeout() time.Duration  { return Master.MCPRequestTimeout }
func EscapeDisambiguation() time.Duration { return Master.EscapeDisambiguation }

// ConsecutiveLimitClamped applies the guard safety bounds to a raw value, so a
// caller that still reads a top-level config field cannot disable the guard.
func ConsecutiveLimitClamped(v int) int {
	if v <= 0 {
		return ConsecutiveLimit()
	}
	return clamp(v, AbsoluteMinConsecutiveLimit, AbsoluteMaxConsecutiveLimit)
}

// ReasoningBudget returns the thinking token budget for an effort level. -1
// means unbounded. Unknown levels fall back to the low default.
func ReasoningBudget(effort string) int {
	if v, ok := Master.ReasoningTokenBudgets[effort]; ok && v != 0 {
		return v
	}
	return DefaultReasoningBudgets()["low"]
}
