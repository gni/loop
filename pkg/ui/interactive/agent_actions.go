package interactive

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"loop/pkg/agent/subagent"
	"loop/pkg/agent/swarm"
	"loop/pkg/ui/style"
)

func createManagedAgent(mam *swarm.MultiAgentManager, sr *SessionReader, signals chan os.Signal, w io.Writer, theme style.UITheme) (bool, error) {
	name, err := readMenuField(sr, signals, w, theme, "Create agent / 1 of 4", "Give this agent a name and a clear focus.", "Name", "", func(name string) error {
		if err := subagent.ValidateAgentName(name); err != nil {
			return err
		}
		for _, existing := range mam.ListAgents() {
			if name == existing {
				return fmt.Errorf("An agent named %q already exists.", name)
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	instructions, err := readMenuField(sr, signals, w, theme, "Create agent / 2 of 4", menuDisplayText(name), "What should this agent focus on?", "", func(value string) error {
		if value == "" {
			return fmt.Errorf("Add instructions before continuing.")
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	parents := append([]string{"Base agent"}, mam.ListAgents()...)
	parentIndex, err := RunInteractiveSelect(sr, signals, w, "Create agent / 3 of 4 · Parent", parents, theme)
	if err != nil {
		return false, &menuInputError{cause: err}
	}
	parent := ""
	if parentIndex > 0 {
		parent = parents[parentIndex]
	}
	skills := []string{"All active skills"}
	for _, skill := range mam.BaseAgent.GetActiveSkills() {
		skills = append(skills, skill.Name)
	}
	skillIndex, err := RunInteractiveSelect(sr, signals, w, "Create agent / 4 of 4 · Skills", skills, theme)
	if err != nil {
		return false, &menuInputError{cause: err}
	}
	var assigned []string
	if skillIndex > 0 {
		assigned = []string{skills[skillIndex]}
	}
	if err := mam.SpawnAgent(name, instructions, parent, assigned); err != nil {
		return false, err
	}
	if !mam.JoinAgent(name) {
		return false, fmt.Errorf("agent created, but could not open its conversation")
	}
	return true, nil
}

func deleteManagedAgent(mam *swarm.MultiAgentManager, name string, sr *SessionReader, signals chan os.Signal, w io.Writer, theme style.UITheme) error {
	if name == "base" {
		return nil
	}
	answer, err := readMenuField(sr, signals, w, theme, "Delete agent", "This stops the agent and removes it from the list.", "Delete "+menuDisplayText(name)+"? Type yes to confirm", "", nil)
	if err != nil {
		return err
	}
	if !strings.EqualFold(answer, "yes") && !strings.EqualFold(answer, "y") {
		return ErrSelectionCancelled
	}
	return mam.RemoveAgent(name)
}

func agentActionNotice(err error) string {
	if err == nil || errors.Is(err, ErrSelectionCancelled) {
		return ""
	}
	return menuDisplayText(err.Error())
}
