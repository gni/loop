package agent

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"loop/pkg/agent/tool"
)

type GuidedPhase string

const (
	GuidedPhaseExplore GuidedPhase = "explore"
	GuidedPhasePlan    GuidedPhase = "plan"
	GuidedPhaseExecute GuidedPhase = "execute"
)

type FileSnapshot struct {
	AbsPath  string
	Existed  bool
	Data     []byte
	Mode     os.FileMode
	Target   string
	Expected string
	Previous string
}

type GuidedController struct {
	mu           sync.RWMutex
	enabled      bool
	phase        GuidedPhase
	planApproved bool
	planKey      string
	snapshots    map[string]FileSnapshot
	journalPath  string
	journalHash  string
	journalErr   error
	journalBlobs map[string]bool
	workspace    string
}

func NewGuidedController(enabled bool) *GuidedController {
	return &GuidedController{
		enabled:   enabled,
		phase:     GuidedPhaseExplore,
		snapshots: make(map[string]FileSnapshot),
	}
}

func (gc *GuidedController) SetEnabled(enabled bool) {
	if gc == nil {
		return
	}
	gc.mu.Lock()
	defer gc.mu.Unlock()
	if enabled && !gc.enabled {
		gc.resetPlanApprovalLocked()
	}
	gc.enabled = enabled
}

func (gc *GuidedController) IsEnabled() bool {
	if gc == nil {
		return false
	}
	gc.mu.RLock()
	defer gc.mu.RUnlock()
	return gc.enabled
}

func (gc *GuidedController) Phase() GuidedPhase {
	if gc == nil {
		return GuidedPhaseExecute
	}
	gc.mu.RLock()
	defer gc.mu.RUnlock()
	if !gc.enabled {
		return GuidedPhaseExecute
	}
	return gc.phase
}

func (gc *GuidedController) IsPlanApproved() bool {
	if gc == nil {
		return false
	}
	gc.mu.RLock()
	defer gc.mu.RUnlock()
	return gc.planApproved
}

func (gc *GuidedController) ResetForNewUserTurn(todos []tool.TodoItem) {
	if gc == nil {
		return
	}
	gc.mu.Lock()
	defer gc.mu.Unlock()
	if !gc.enabled {
		return
	}
	allDone := true
	for _, t := range todos {
		if !strings.EqualFold(strings.TrimSpace(t.Status), "completed") {
			allDone = false
			break
		}
	}
	if len(todos) == 0 || allDone {
		gc.resetPlanApprovalLocked()
	}
}

func (gc *GuidedController) resetPlanApproval() {
	if gc == nil {
		return
	}
	gc.mu.Lock()
	defer gc.mu.Unlock()
	gc.resetPlanApprovalLocked()
}

func (gc *GuidedController) resetPlanApprovalLocked() {
	gc.phase = GuidedPhaseExplore
	gc.planApproved = false
	gc.planKey = ""
}

func (gc *GuidedController) CheckToolAllowed(a *Agent, toolName string) (bool, error) {
	var todos []tool.TodoItem
	if a != nil {
		todos = a.GetTodos()
	}
	return gc.CheckPlanAllowed(todos, toolName)
}

// CheckPlanAllowed uses the calling agent's plan rather than shared base state.
func (gc *GuidedController) CheckPlanAllowed(todos []tool.TodoItem, toolName string) (bool, error) {
	if gc == nil || !gc.IsEnabled() {
		return false, nil
	}
	if !tool.PolicyForName(toolName, "").RequiresPlan {
		return false, nil
	}
	if len(todos) == 0 {
		gc.mu.Lock()
		gc.phase = GuidedPhaseExplore
		gc.mu.Unlock()
		return false, fmt.Errorf("guided mode: inspect files with read/grep/find_files/list_directory and register a plan with 'todo' before calling '%s'", toolName)
	}

	currentPlan := guidedPlanKey(todos)
	gc.mu.Lock()
	defer gc.mu.Unlock()
	if !gc.planApproved || gc.planKey != currentPlan {
		gc.phase = GuidedPhasePlan
		return true, nil
	}
	gc.phase = GuidedPhaseExecute
	return false, nil
}

func (gc *GuidedController) ApprovePlan(todos []tool.TodoItem) {
	if gc == nil {
		return
	}
	gc.mu.Lock()
	defer gc.mu.Unlock()
	gc.planApproved = true
	gc.planKey = guidedPlanKey(todos)
	gc.phase = GuidedPhaseExecute
}

func FormatPlanSummary(todos []tool.TodoItem) string {
	var sb strings.Builder
	sb.WriteString("Proposed execution plan:\n")
	for i, item := range todos {
		status := item.Status
		if status == "" {
			status = "pending"
		}
		sb.WriteString(fmt.Sprintf("  %d. [%s] %s\n", i+1, status, item.Task))
	}
	sb.WriteString("\nApprove this plan to begin execution?")
	return sb.String()
}

func guidedPlanKey(todos []tool.TodoItem) string {
	var sb strings.Builder
	for _, t := range todos {
		// Lengths separate task fields even when they contain delimiters.
		fmt.Fprintf(&sb, "%d:%s%d:%s", len(t.ID), t.ID, len(t.Task), t.Task)
	}
	return sb.String()
}
