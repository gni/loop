package swarm

import (
	"context"
	"io"
	"strings"

	"loop/pkg/agent/tool"
)

type multiAgentContext struct {
	tool.AgentContext
	ma *MultiAgent
}

func (mac *multiAgentContext) Context() context.Context {
	mac.ma.HistoryMu.RLock()
	ctx := mac.ma.ActiveContext
	mac.ma.HistoryMu.RUnlock()
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

func (mac *multiAgentContext) RecordMutation(absPath string, data []byte) {
	if fo, ok := mac.AgentContext.(tool.FileObserver); ok {
		fo.RecordMutation(absPath, data)
	}
}

func (mac *multiAgentContext) GetTodos() []tool.TodoItem {
	if ts, ok := mac.AgentContext.(tool.TodoState); ok {
		return ts.GetTodos()
	}
	return nil
}

func (mac *multiAgentContext) SetTodos(todos []tool.TodoItem) error {
	if ts, ok := mac.AgentContext.(tool.TodoState); ok {
		return ts.SetTodos(todos)
	}
	return nil
}

func (mac *multiAgentContext) AskUser(question string, options []tool.AskUserOption, recommended string) (string, error) {
	if uq, ok := mac.AgentContext.(tool.UserInquirer); ok {
		return uq.AskUser(question, options, recommended)
	}
	if recommended != "" {
		return recommended, nil
	}
	if len(options) > 0 {
		return options[0].Label, nil
	}
	return "Confirmed", nil
}

func (mac *multiAgentContext) GetActiveSkills() []tool.Skill {
	mac.ma.HistoryMu.RLock()
	defer mac.ma.HistoryMu.RUnlock()
	return mac.ma.Skills
}

func (mac *multiAgentContext) ReloadSkills() []tool.Skill {
	reloaded := mac.AgentContext.ReloadSkills()
	mac.ma.HistoryMu.Lock()
	localNames := make(map[string]struct{}, len(mac.ma.LocalSkills))
	for _, localSkill := range mac.ma.LocalSkills {
		localNames[strings.ToLower(localSkill.Name)] = struct{}{}
	}
	if mac.ma.HasAllSkills {
		mac.ma.Skills = make([]tool.Skill, 0, len(reloaded)+len(mac.ma.LocalSkills))
		mac.ma.Skills = append(mac.ma.Skills, reloaded...)
		mac.ma.Skills = append(mac.ma.Skills, mac.ma.LocalSkills...)
	} else {
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
	}
	mac.ma.HistoryMu.Unlock()
	return reloaded
}
