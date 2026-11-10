package tool

import (
	"context"
	"io"
	"time"
)

type ExecutionEnvironment interface{ GetExecutionEnv() map[string]string }
type ShellExecutionGate interface{ IsShellExecutionAllowed() bool }
type ShellScanSettings interface{ IsShellScanDefaultsEnabled() bool }
type PersistentShellSettings interface{ IsPersistentBashEnabled() bool }
type ShellTimeoutSettings interface{ GetShellTimeout() time.Duration }
type FullTaskStatusProvider interface {
	GetFullTaskStatus(string) (string, string, error)
}
type TaskOutputVersionProvider interface{ GetTaskOutputVersion(string) int64 }
type TaskCommandProvider interface{ GetTaskCommand(string) string }
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
