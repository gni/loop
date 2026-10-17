package repl

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/domain/tool"
	"loop/pkg/ui"
	"loop/pkg/ui/commands"
)

var baseAutocompleteCandidates = []string{
	"/guided", "/guided on", "/guided off", "/guided status", "/guided plan", "/guided approve",
	"/plan", "/rollback", "/undo",
	"/config ", "/config show", "/config set ",
	"/clear", "/skills", "/skills load ",
	"/session ", "/session list", "/session new", "/session load", "/session branch ", "/session clear",
	"/help", "/commands", "/exit", "/quit", "/mcp",
	"/task ", "/task list", "/task view ", "/task stream ", "/task kill ", "/task remove ", "/task clear",
	"/task_kill ", "/task_status ", "/task_list", "/task_clear",
	"/kill ", "/stop", "/stop ", "/status", "/status ", "/taskstop ", "/tasks", "/ps",
	"/toggle", "/collapse", "/expand",
	"/provider ", "/provider list", "/provider add ", "/provider select ", "/provider model ", "/provider timeout ", "/provider remove ", "/p ",
	"/templates", "/plugins", "/extensions", "/reload",
	"/agent ", "/agent list", "/agent join ", "/agent spawn ",
	"/agents ", "/agents list", "/agents join ", "/agents spawn ",
	"/stats", "/tokens", "/usage", "/compress",
	"/context", "/context auto", "/context off", "/ctx",
	"/tools", "/tools list", "/tools disable ", "/tools enable ", "/tools toggle ", "/tools reset", "/tools menu", "/tool",
}

var configAutocompleteKeys = []string{
	"endpoint", "model", "temperature", "guided_mode", "disable_file_observation_guard", "auto_approve", "show_thinking",
	"collapse_results", "show_tokens", "theme", "syntax_theme", "context_limit", "steps",
	"direct_commands", "cert_file", "key_file", "skip_verify", "reasoning_effort",
	"before_tool_hook", "after_tool_hook", "debug", "debug_file",
	"max_paste_lines", "max_paste_chars", "auto_adapt_context", "min_context_window",
	"approval_always_answer", "ask_user_mode", "recap_interval", "disable_recap",
	"prompts_file", "disabled_tools",
}

func matchPrefixWithSpace(candidates []string, filter string, spaceSet map[string]bool) []string {
	var out []string
	for _, c := range candidates {
		if strings.HasPrefix(c, filter) {
			if spaceSet[c] {
				out = append(out, c+" ")
			} else {
				out = append(out, c)
			}
		}
	}
	return out
}

func firstMatchingPrefix(s string, prefixes ...string) (string, bool) {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return p, true
		}
	}
	return "", false
}

func autoCompleteCallback(line string, pos int, key rune, a *agent.Agent) (string, int, bool) {
	if key != '\t' {
		return line, pos, false
	}

	prefixToPos := line[:pos]
	wordStartIdx := strings.LastIndex(prefixToPos, " ") + 1
	wordToComplete := prefixToPos[wordStartIdx:]

	var matches []string
	if strings.HasPrefix(wordToComplete, "/") {
		seen := make(map[string]bool)
		addCandidate := func(cand string) {
			if strings.HasPrefix(cand, prefixToPos) && !seen[cand] {
				seen[cand] = true
				matches = append(matches, cand)
			}
		}
		for _, c := range baseAutocompleteCandidates {
			addCandidate(c)
		}
		for _, t := range tool.LoadPromptTemplates(commands.PromptTemplateCommandDir()) {
			addCandidate("/" + t.Name)
		}
		workspaceRoot := ""
		if a != nil {
			workspaceRoot = a.GetWorkspaceRoot()
		}
		for _, ext := range ui.ListExtensions(workspaceRoot) {
			addCandidate(ext.Name)
		}
	}

	if len(matches) == 0 {
		switch {
		case strings.HasPrefix(prefixToPos, "/guided "):
			matches = matchPrefixWithSpace([]string{"on", "off", "status", "plan", "approve"}, strings.TrimPrefix(prefixToPos, "/guided "), nil)

		case strings.HasPrefix(prefixToPos, "/config "):
			isSet := strings.HasPrefix(prefixToPos, "/config set ")
			filterPrefix := strings.TrimPrefix(prefixToPos, "/config ")
			var candidates []string
			if isSet {
				filterPrefix = strings.TrimPrefix(prefixToPos, "/config set ")
			} else {
				candidates = append(candidates, "show", "set")
			}
			candidates = append(candidates, configAutocompleteKeys...)
			for _, c := range candidates {
				if strings.HasPrefix(c, filterPrefix) {
					if c != "show" && c != "set" {
						matches = append(matches, c+" ")
					} else {
						matches = append(matches, c)
					}
				}
			}

		case strings.HasPrefix(prefixToPos, "/session "):
			matches = matchPrefixWithSpace(
				[]string{"list", "new", "load", "branch", "clear"},
				strings.TrimPrefix(prefixToPos, "/session "),
				map[string]bool{"branch": true, "load": true},
			)

		case strings.HasPrefix(prefixToPos, "/skills load ") && a != nil:
			filterPrefix := strings.TrimPrefix(prefixToPos, "/skills load ")
			for _, s := range a.ActiveSkills {
				if strings.HasPrefix(s.Name, filterPrefix) {
					matches = append(matches, s.Name)
				}
			}

		case strings.HasPrefix(prefixToPos, "/skills "):
			matches = matchPrefixWithSpace([]string{"load", "reload"}, strings.TrimPrefix(prefixToPos, "/skills "), map[string]bool{"load": true})

		default:
			if cmdPrefix, ok := firstMatchingPrefix(prefixToPos, "/provider ", "/providers ", "/p "); ok {
				filterPrefix := strings.TrimPrefix(prefixToPos, cmdPrefix)
				providerKeys := []string{"default"}
				if a != nil && a.Config != nil {
					for k := range a.Config.Providers {
						providerKeys = append(providerKeys, k)
					}
				}
				if subCmd, hasSub := firstMatchingPrefix(filterPrefix, "select ", "use ", "remove ", "delete "); hasSub {
					provPrefix := strings.TrimPrefix(filterPrefix, subCmd)
					for _, pk := range providerKeys {
						if strings.HasPrefix(pk, provPrefix) {
							matches = append(matches, subCmd+pk)
						}
					}
				} else {
					for _, pk := range providerKeys {
						if strings.HasPrefix(pk, filterPrefix) {
							matches = append(matches, pk)
						}
					}
					matches = append(matches, matchPrefixWithSpace(
						[]string{"list", "add", "select", "use", "model", "timeout", "remove", "delete"},
						filterPrefix,
						map[string]bool{"add": true, "select": true, "use": true, "model": true, "timeout": true, "remove": true, "delete": true},
					)...)
				}
			} else if cmdPrefix, ok := firstMatchingPrefix(prefixToPos, "/tools ", "/tool "); ok {
				filterPrefix := strings.TrimPrefix(prefixToPos, cmdPrefix)
				if subCmd, hasSub := firstMatchingPrefix(filterPrefix, "disable ", "enable ", "toggle "); hasSub {
					toolPrefix := strings.TrimPrefix(filterPrefix, subCmd)
					if subCmd == "enable " && strings.HasPrefix("all", toolPrefix) {
						matches = append(matches, "all")
					}
					if a != nil && a.Registry != nil {
						var toolNames []string
						for name := range a.Registry.GetAllExecutors() {
							toolNames = append(toolNames, name)
						}
						sort.Strings(toolNames)
						for _, name := range toolNames {
							if strings.HasPrefix(name, toolPrefix) {
								matches = append(matches, name)
							}
						}
					}
				} else {
					matches = matchPrefixWithSpace(
						[]string{"list", "disable", "enable", "toggle", "reset", "menu"},
						filterPrefix,
						map[string]bool{"disable": true, "enable": true, "toggle": true},
					)
				}
			} else if cmdPrefix, ok := firstMatchingPrefix(prefixToPos, "/task ", "/stop ", "/kill ", "/status ", "/taskstop ", "/task_kill ", "/task_status "); ok {
				filterPrefix := strings.TrimPrefix(prefixToPos, cmdPrefix)
				appendTaskIDs := func(prefix, idFilter string) {
					if a == nil {
						return
					}
					for _, t := range a.ListTasks() {
						if strings.HasPrefix(t.ID, idFilter) {
							matches = append(matches, prefix+t.ID)
						}
					}
				}
				if cmdPrefix == "/task " {
					if sub, hasSub := firstMatchingPrefix(filterPrefix, "view ", "stream ", "kill ", "remove "); hasSub {
						appendTaskIDs(sub, strings.TrimPrefix(filterPrefix, sub))
					}
					if len(matches) == 0 {
						matches = matchPrefixWithSpace(
							[]string{"list", "view", "stream", "kill", "remove", "clear"},
							filterPrefix,
							map[string]bool{"view": true, "stream": true, "kill": true, "remove": true},
						)
						appendTaskIDs("", filterPrefix)
					}
				} else {
					appendTaskIDs("", filterPrefix)
				}
			}
		}
	}

	if len(matches) == 0 {
		dirPart, filePart := filepath.Split(wordToComplete)
		searchDir := dirPart
		if searchDir == "" {
			searchDir = "."
		}
		if entries, err := os.ReadDir(searchDir); err == nil {
			for _, entry := range entries {
				name := entry.Name()
				if strings.HasPrefix(name, filePart) {
					fullName := dirPart + name
					if entry.IsDir() {
						fullName += "/"
					}
					matches = append(matches, fullName)
				}
			}
		}
	}

	if len(matches) == 0 {
		return line, pos, false
	}

	if len(matches) == 1 {
		completedLine := line[:wordStartIdx] + matches[0] + line[pos:]
		return completedLine, wordStartIdx + len(matches[0]), true
	}

	commonPrefix := matches[0]
	for _, m := range matches[1:] {
		for i := 0; i < len(commonPrefix) && i < len(m); i++ {
			if commonPrefix[i] != m[i] {
				commonPrefix = commonPrefix[:i]
				break
			}
		}
		if len(commonPrefix) > len(m) {
			commonPrefix = m
		}
	}

	if len(commonPrefix) > len(wordToComplete) {
		completedLine := line[:wordStartIdx] + commonPrefix + line[pos:]
		return completedLine, wordStartIdx + len(commonPrefix), true
	}

	return line, pos, false
}
