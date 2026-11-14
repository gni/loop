package tool

import (
	"context"
	"io"
	"time"

	"loop/pkg/domain/evidence"
)

type ExecutionEnvironment interface{ GetExecutionEnv() map[string]string }
type ShellExecutionGate interface{ IsShellExecutionAllowed() bool }
type ShellScanSettings interface{ IsShellScanDefaultsEnabled() bool }
type PersistentShellSettings interface{ IsPersistentBashEnabled() bool }
type ShellTimeoutSettings interface{ GetShellTimeout() time.Duration }
type SyntaxValidationSettings interface{ IsStrictSyntaxEnabled() bool }
type FullTaskStatusProvider interface {
	GetFullTaskStatus(string) (string, string, error)
}
type TaskOutputVersionProvider interface{ GetTaskOutputVersion(string) int64 }
type TaskCommandProvider interface{ GetTaskCommand(string) string }

// BackgroundTaskSnapshot contains one observed process state. Byte counts are
// cumulative stream positions, including bytes no longer retained in the tails.
type BackgroundTaskSnapshot struct {
	ID, Command, Status string
	Stdout, Stderr      string
	StdoutBytes         int64
	StderrBytes         int64
	StartedAt           int64
	Diagnostic          string
	Evidence            *evidence.Record
}

// TaskOutputSnapshotProvider observes status, output and evidence atomically.
// A nil snapshot without an error means this optional capability is unavailable.
type TaskOutputSnapshotProvider interface {
	GetTaskOutputSnapshot(id string, full bool) (*BackgroundTaskSnapshot, error)
}

type SnapshotGuard interface {
	HasSnapshot(string) bool
	DiscardSnapshot(string)
}
type BackgroundTaskSpawner interface {
	SpawnTaskWithOptions(context.Context, string, io.Writer, BackgroundTaskOptions) (string, error)
}
type ContextTaskSpawner interface {
	SpawnTaskContext(context.Context, string, io.Writer) (string, error)
}
