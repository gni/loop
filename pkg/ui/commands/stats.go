package commands

import (
	"io"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/db"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func handleStatsCommand(a *agent.Agent, messages *[]db.Message, theme *style.UITheme, w io.Writer) {
	var msgs []db.Message
	if messages != nil {
		msgs = *messages
	}
	if mam, ok := a.MultiAgentManager.(*swarm.MultiAgentManager); ok && mam != nil {
		var timings *agent.TimingRecorder
		if mam.BaseAgent != nil {
			timings = mam.BaseAgent.Timing
		}
		render.RenderSwarmStats(w, msgs, mam.UsageSnapshot(), timings, *theme)
		return
	}
	model := ""
	if a.Config != nil {
		model = a.Config.Model
	}
	render.RenderStats(w, msgs, model, a.Timing, *theme)
}
