package agent

import (
	"encoding/json"
	"fmt"
	"loop/pkg/agent/tool"
	"sort"
	"strings"
)

type toolSearch struct{ agent *Agent }

func (t *toolSearch) Name() string { return "search_tools" }
func (t *toolSearch) Definition() tool.Tool {
	return tool.NewFunctionTool(t.Name(), "Discover and expose matching tools from configured integrations", map[string]tool.SchemaProp{"query": tool.StringProp("Search words in tool names and descriptions"), "server": tool.StringProp("Optional configured server name"), "limit": tool.NumberProp("Maximum results, up to 20")}, "query")
}
func (t *toolSearch) Execute(ctx tool.AgentContext, arguments string) (string, error) {
	var args struct {
		Query  string `json:"query"`
		Server string `json:"server"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Query) == "" {
		return "", fmt.Errorf("query must not be empty")
	}
	names := t.agent.DeferredMCPServers()
	if args.Server != "" {
		names = []string{args.Server}
	}
	for _, name := range names {
		if _, err := t.agent.connectMCPServer(name); err != nil {
			return "", err
		}
	}
	type match struct {
		Definition tool.Tool
		Score      int
	}
	var matches []match
	words := strings.Fields(strings.ToLower(args.Query))
	for name, executor := range t.agent.Registry.GetAllExecutors() {
		if name == t.Name() || t.agent.Registry.IsDisabled(name) || !tool.IsAllowedTool(name, t.agent.ToolSelection) {
			continue
		}
		definition := executor.Definition()
		haystack := strings.ToLower(name + " " + definition.Function.Description)
		score := 0
		for _, word := range words {
			if strings.Contains(haystack, word) {
				score++
			}
		}
		if score > 0 {
			matches = append(matches, match{definition, score})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score == matches[j].Score {
			return matches[i].Definition.Function.Name < matches[j].Definition.Function.Name
		}
		return matches[i].Score > matches[j].Score
	})
	limit := args.Limit
	if limit <= 0 || limit > 20 {
		limit = 20
	}
	var definitions []tool.Tool
	for _, entry := range matches[:min(len(matches), limit)] {
		t.agent.Registry.Expose(entry.Definition.Function.Name)
		definitions = append(definitions, entry.Definition)
	}
	t.agent.markSystemPromptDirty()
	data, err := json.Marshal(definitions)
	return string(data), err
}
