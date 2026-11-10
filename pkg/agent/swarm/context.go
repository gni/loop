package swarm

import (
	"context"
	"io"
	"os"
	"strings"
	"time"

	"loop/pkg/agent/tool"
	bashtool "loop/pkg/agent/tool/bash"
	"loop/pkg/domain/limits"
	domaintool "loop/pkg/domain/tool"
)

type multiAgentContext struct {
	tool.AgentContext
	ma *MultiAgent
}

func (mac *multiAgentContext) BeginFileMutation(ctx context.Context, path string, data []byte, mode os.FileMode) (func(), error) {
	if recorder, ok := mac.AgentContext.(domaintool.FileMutationRecorder); ok {
		return recorder.BeginFileMutation(ctx, path, data, mode)
	}
	return func() {}, nil
}

func (mac *multiAgentContext) DedicatedBashTool() tool.ToolExecutor {
	if mac == nil || mac.ma == nil {
		return nil
	}
	return mac.ma.BashTool
}

func (mac *multiAgentContext) GetExecutionEnv() map[string]string {
	if mac == nil || mac.ma == nil || mac.ma.BaseAgent == nil {
		return nil
	}
	env := mac.ma.BaseAgent.GetExecutionEnv()
	if env == nil {
		env = make(map[string]string)
	}
	if provider, ok := mac.AgentContext.(domaintool.ExecutionEnvironment); ok {
		for key, value := range provider.GetExecutionEnv() {
			env[key] = value
		}
	}
	env["LOOP_SUBAGENT"] = mac.ma.Name
	return env
}

func (mac *multiAgentContext) SpawnTask(command string, w io.Writer) (string, error) {
	return mac.SpawnTaskWithOptions(mac.Context(), command, w, tool.BackgroundTaskOptions{})
}

func (mac *multiAgentContext) SpawnTaskWithOptions(ctx context.Context, command string, w io.Writer, options tool.BackgroundTaskOptions) (string, error) {
	ownerContext := mac.Context()
	if err := ownerContext.Err(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ownerContext, cancel)
	defer stop()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var id string
	var err error
	if spawner, ok := mac.AgentContext.(domaintool.BackgroundTaskSpawner); ok {
		environment := mac.GetExecutionEnv()
		for key, value := range options.Environment {
			environment[key] = value
		}
		environment["LOOP_SUBAGENT"] = mac.ma.Name
		options.Environment = environment
		id, err = spawner.SpawnTaskWithOptions(ctx, command, w, options)
	} else {
		if options.Directory != "" && options.Directory != mac.GetWorkspaceRoot() {
			directory, err := mac.SafePath(options.Directory)
			if err != nil {
				return "", err
			}
			command = "cd -- " + bashtool.ShellQuote(directory) + " && {\n" + command + "\n}"
		}
		if spawner, ok := mac.AgentContext.(domaintool.ContextTaskSpawner); ok {
			id, err = spawner.SpawnTaskContext(ctx, command, w)
		} else {
			id, err = mac.AgentContext.SpawnTask(command, w)
		}
	}
	if err == nil && mac.ma != nil {
		mac.ma.RecordTask(id)
	}
	return id, err
}

func (mac *multiAgentContext) IsShellExecutionAllowed() bool {
	gate, ok := mac.AgentContext.(domaintool.ShellExecutionGate)
	return ok && gate.IsShellExecutionAllowed()
}

func (mac *multiAgentContext) IsShellScanDefaultsEnabled() bool {
	settings, ok := mac.AgentContext.(domaintool.ShellScanSettings)
	return ok && settings.IsShellScanDefaultsEnabled()
}

func (mac *multiAgentContext) IsPersistentBashEnabled() bool {
	settings, ok := mac.AgentContext.(domaintool.PersistentShellSettings)
	return !ok || settings.IsPersistentBashEnabled()
}

func (mac *multiAgentContext) IsAtomicWritesEnabled() bool {
	settings, ok := mac.AgentContext.(domaintool.AtomicWriteSettings)
	return !ok || settings.IsAtomicWritesEnabled()
}

func (mac *multiAgentContext) GetShellTimeout() time.Duration {
	if settings, ok := mac.AgentContext.(domaintool.ShellTimeoutSettings); ok {
		return settings.GetShellTimeout()
	}
	return 120 * time.Second
}

func (mac *multiAgentContext) GetLimits() limits.Limits {
	return limits.For(mac.AgentContext)
}

func (mac *multiAgentContext) GetFullTaskStatus(id string) (string, string, error) {
	if provider, ok := mac.AgentContext.(domaintool.FullTaskStatusProvider); ok {
		return provider.GetFullTaskStatus(id)
	}
	return mac.AgentContext.GetTaskStatus(id)
}

func (mac *multiAgentContext) GetTaskOutputVersion(id string) int64 {
	if provider, ok := mac.AgentContext.(domaintool.TaskOutputVersionProvider); ok {
		return provider.GetTaskOutputVersion(id)
	}
	return 0
}

func (mac *multiAgentContext) Context() context.Context {
	mac.ma.HistoryMu.RLock()
	ctx := mac.ma.ActiveContext
	mac.ma.HistoryMu.RUnlock()
	// A cancelled task context must stay cancelled. Falling back to the
	// agent lifetime here would let an interrupted tool continue mutating.
	if ctx != nil {
		return ctx
	}
	return mac.ma.Context
}

func (mac *multiAgentContext) GetLiveWriter() io.Writer {
	if low, ok := mac.AgentContext.(tool.LiveOutputWriter); ok {
		return low.GetLiveWriter()
	}
	return nil
}

func (mac *multiAgentContext) SetLiveBodyStreamed(streamed bool) {
	if low, ok := mac.AgentContext.(tool.LiveOutputWriter); ok {
		low.SetLiveBodyStreamed(streamed)
	}
}

func (mac *multiAgentContext) DidStreamLiveBody() bool {
	if low, ok := mac.AgentContext.(tool.LiveOutputWriter); ok {
		return low.DidStreamLiveBody()
	}
	return false
}

func (mac *multiAgentContext) RecordRead(absPath string, data []byte) {
	if fo, ok := mac.AgentContext.(tool.FileObserver); ok {
		fo.RecordRead(absPath, data)
	}
}

func (mac *multiAgentContext) CheckMutationAllowed(absPath string, isEdit bool) error {
	if fo, ok := mac.AgentContext.(tool.FileObserver); ok {
		return fo.CheckMutationAllowed(absPath, isEdit)
	}
	return nil
}

func (mac *multiAgentContext) InvalidateCodeIndex(path string) {
	if index, ok := mac.AgentContext.(domaintool.CodeIndexInvalidator); ok {
		index.InvalidateCodeIndex(path)
	}
}

func (mac *multiAgentContext) RecordMutation(absPath string, data []byte) {
	if fo, ok := mac.AgentContext.(tool.FileObserver); ok {
		fo.RecordMutation(absPath, data)
	}
}

func (mac *multiAgentContext) HasSnapshot(absPath string) bool {
	if sg, ok := mac.AgentContext.(interface{ HasSnapshot(string) bool }); ok {
		return sg.HasSnapshot(absPath)
	}
	return false
}

func (mac *multiAgentContext) DiscardSnapshot(absPath string) {
	if sd, ok := mac.AgentContext.(interface{ DiscardSnapshot(string) }); ok {
		sd.DiscardSnapshot(absPath)
	}
}

func (mac *multiAgentContext) GetTodos() []tool.TodoItem { return mac.ma.GetTodos() }

func (mac *multiAgentContext) GetTaskCommand(taskID string) string {
	if tc, ok := mac.AgentContext.(domaintool.TaskCommandProvider); ok {
		return tc.GetTaskCommand(taskID)
	}
	return ""
}

func (mac *multiAgentContext) SetTodos(todos []tool.TodoItem) error { return mac.ma.SetTodos(todos) }

func (mac *multiAgentContext) AskUser(question string, options []tool.AskUserOption, recommended string) (string, error) {
	if uq, ok := mac.AgentContext.(tool.UserInquirer); ok {
		return uq.AskUser(question, options, recommended)
	}
	return tool.DefaultAskUserSelection(options, recommended), nil
}

func (mac *multiAgentContext) GetActiveSkills() []tool.Skill {
	return mac.ma.GetEffectiveSkills()
}

func (mac *multiAgentContext) ReloadSkills() []tool.Skill {
	reloaded := mac.AgentContext.ReloadSkills()
	mac.ma.HistoryMu.Lock()
	defer mac.ma.HistoryMu.Unlock()

	localNames := make(map[string]struct{}, len(mac.ma.LocalSkills))
	for _, localSkill := range mac.ma.LocalSkills {
		localNames[strings.ToLower(localSkill.Name)] = struct{}{}
	}
	if mac.ma.HasAllSkills {
		mac.ma.Skills = make([]tool.Skill, 0, len(reloaded)+len(mac.ma.LocalSkills))
		mac.ma.Skills = append(mac.ma.Skills, reloaded...)
		mac.ma.Skills = append(mac.ma.Skills, mac.ma.LocalSkills...)
		return reloaded
	}

	for i, existing := range mac.ma.Skills {
		if _, isLocal := localNames[strings.ToLower(existing.Name)]; isLocal {
			continue
		}
		for _, r := range reloaded {
			if r.Name == existing.Name {
				mac.ma.Skills[i] = r
				break
			}
		}
	}
	return reloaded
}
