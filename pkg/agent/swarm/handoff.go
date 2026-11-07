package swarm

import (
	"fmt"
	"path/filepath"
	"strings"

	"loop/pkg/agent/tool"
)

const maxHandoffBytes = 16 << 10

type handoffArguments struct {
	Prompt         string   `json:"prompt"`
	Task           string   `json:"task"`
	Input          string   `json:"input"`
	Message        string   `json:"message"`
	Constraints    []string `json:"constraints"`
	RelevantPaths  []string `json:"relevant_paths"`
	OutputContract string   `json:"output_contract"`
}

func prepareHandoff(ctx tool.AgentContext, arguments string, inherited []string) (string, error) {
	if len(arguments) > maxHandoffBytes*6 {
		return "", fmt.Errorf("handoff arguments exceed the size limit; send a focused task and paths")
	}
	if err := ctx.Context().Err(); err != nil {
		return "", err
	}
	var raw string
	var args handoffArguments
	if err := tool.ParseToolArguments(arguments, &args, func(v string) { raw = v }); err != nil {
		return "", fmt.Errorf("parse subagent handoff: %w", err)
	}
	prompt := strings.TrimSpace(tool.FirstNonEmpty(raw, args.Prompt, args.Task, args.Input, args.Message))
	if prompt == "" {
		return "", fmt.Errorf("missing required argument: prompt")
	}
	if len(args.Constraints) > 16 || len(args.RelevantPaths) > 16 {
		return "", fmt.Errorf("handoff accepts up to 16 constraints and 16 relevant paths; narrow the delegated task")
	}
	var handoff strings.Builder
	handoff.WriteString("Task:\n" + prompt)
	if len(args.Constraints) > 0 {
		handoff.WriteString("\n\nConstraints:\n")
		for _, constraint := range args.Constraints {
			if value := strings.TrimSpace(constraint); value != "" {
				handoff.WriteString("- " + value + "\n")
			}
		}
	}
	if len(args.RelevantPaths) > 0 {
		handoff.WriteString("\n\nRelevant workspace paths (read as needed):\n")
		for _, path := range args.RelevantPaths {
			if strings.TrimSpace(path) == "" {
				return "", fmt.Errorf("relevant_paths must not contain empty paths")
			}
			safe, err := ctx.SafePath(path)
			if err != nil {
				return "", fmt.Errorf("handoff path %q: %w", path, err)
			}
			rel, err := filepath.Rel(ctx.GetWorkspaceRoot(), safe)
			if err != nil {
				return "", err
			}
			handoff.WriteString("- " + filepath.ToSlash(rel) + "\n")
		}
	}
	contract := strings.TrimSpace(args.OutputContract)
	if contract == "" {
		contract = "Return a brief result with changes or findings and file:line locations, checks actually run and their outcomes, and any unresolved issues. Distinguish observed tool effects from task verification. Read relevant current files before editing."
	}
	handoff.WriteString("\n\nExpected result:\n" + contract)
	inheritedBytes := 0
	for _, instruction := range inherited {
		inheritedBytes += len(instruction)
	}
	if handoff.Len()+inheritedBytes > maxHandoffBytes {
		return "", fmt.Errorf("handoff and inherited user instructions require %d bytes; limit is %d. Narrow the delegated task or keep it in the parent agent. User instructions will not be silently truncated", handoff.Len()+inheritedBytes, maxHandoffBytes)
	}
	return handoff.String(), nil
}
