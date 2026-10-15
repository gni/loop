package repl

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/domain/tool"
	"loop/pkg/ui/commands"
)

// autoCompleteCallback handles Tab completion for slash commands, configurations,
// sessions, providers, skills, and filesystem paths.
func autoCompleteCallback(line string, pos int, key rune, a *agent.Agent) (string, int, bool) {
	if key != '\t' {
		return line, pos, false
	}

	prefixToPos := line[:pos]
	lastSpaceIdx := strings.LastIndex(prefixToPos, " ")
	wordStartIdx := lastSpaceIdx + 1
	wordToComplete := prefixToPos[wordStartIdx:]

	templateCandidates := []string{}
	for _, t := range tool.LoadPromptTemplates(commands.PromptTemplateCommandDir()) {
		templateCandidates = append(templateCandidates, "/"+t.Name)
	}

	candidates := []string{
		"/config ",
		"/config show",
		"/config set ",
		"/clear",
		"/skills",
		"/skills load ",
		"/session ",
		"/session list",
		"/session new",
		"/session load",
		"/session branch ",
		"/session clear",
		"/help",
		"/commands",
		"/exit",
		"/quit",
		"/mcp",
		"/task ",
		"/task list",
		"/task view ",
		"/task stream ",
		"/task kill ",
		"/task remove ",
		"/task clear",
		"/task_kill ",
		"/task_status ",
		"/task_list",
		"/task_clear",
		"/kill ",
		"/stop",
		"/stop ",
		"/status",
		"/status ",
		"/taskstop ",
		"/tasks",
		"/ps",
		"/toggle",
		"/collapse",
		"/expand",
		"/provider ",
		"/provider list",
		"/provider add ",
		"/provider select ",
		"/provider model ",
		"/provider timeout ",
		"/provider remove ",
		"/p ",
		"/templates",
		"/agent ",
		"/agent list",
		"/agent join ",
		"/agent spawn ",
		"/agents ",
		"/agents list",
		"/agents join ",
		"/agents spawn ",
		"/stats",
		"/tokens",
		"/usage",
		"/compress",
		"/context",
		"/context auto",
		"/context off",
		"/ctx",
		"/tools",
		"/tools list",
		"/tools disable ",
		"/tools enable ",
		"/tools toggle ",
		"/tools reset",
		"/tools menu",
		"/tool",
	}
	candidates = append(candidates, templateCandidates...)

	var matches []string

	if strings.HasPrefix(wordToComplete, "/") {
		for _, c := range candidates {
			if strings.HasPrefix(c, line[:pos]) {
				matches = append(matches, c)
			}
		}
	}

	if len(matches) == 0 {
		if strings.HasPrefix(prefixToPos, "/config ") {
			isSet := strings.HasPrefix(prefixToPos, "/config set ")
			var configCandidates []string
			configKeys := []string{
				"endpoint", "model", "temperature", "auto_approve", "show_thinking",
				"collapse_results", "show_tokens", "theme", "syntax_theme", "context_limit", "steps",
				"direct_commands", "cert_file", "key_file", "skip_verify", "reasoning_effort",
				"before_tool_hook", "after_tool_hook", "debug", "debug_file",
				"max_paste_lines", "max_paste_chars",
				"auto_adapt_context", "min_context_window",
				"approval_always_answer", "ask_user_mode", "recap_interval", "disable_recap",
				"prompts_file", "disabled_tools",
			}
			if !isSet {
				configCandidates = append(configCandidates, "show", "set")
			}
			configCandidates = append(configCandidates, configKeys...)

			var filterPrefix string
			if isSet {
				filterPrefix = strings.TrimPrefix(prefixToPos, "/config set ")
			} else {
				filterPrefix = strings.TrimPrefix(prefixToPos, "/config ")
			}

			for _, c := range configCandidates {
				if strings.HasPrefix(c, filterPrefix) {
					match := c
					if c != "show" && c != "set" {
						match += " "
					}
					matches = append(matches, match)
				}
			}
		} else if strings.HasPrefix(prefixToPos, "/session ") {
			sessionSubcommands := []string{"list", "new", "load", "branch", "clear"}
			filterPrefix := strings.TrimPrefix(prefixToPos, "/session ")
			for _, c := range sessionSubcommands {
				if strings.HasPrefix(c, filterPrefix) {
					match := c
					if c == "branch" || c == "load" {
						match += " "
					}
					matches = append(matches, match)
				}
			}
		} else if strings.HasPrefix(prefixToPos, "/skills ") {
			skillsSubcommands := []string{"load"}
			filterPrefix := strings.TrimPrefix(prefixToPos, "/skills ")
			for _, c := range skillsSubcommands {
				if strings.HasPrefix(c, filterPrefix) {
					match := c
					if c == "load" {
						match += " "
					}
					matches = append(matches, match)
				}
			}
		} else if strings.HasPrefix(prefixToPos, "/provider ") || strings.HasPrefix(prefixToPos, "/providers ") || strings.HasPrefix(prefixToPos, "/p ") {
			providerSubcommands := []string{"list", "add", "select", "use", "model", "timeout", "remove", "delete"}
			cmdPrefix := "/provider "
			if strings.HasPrefix(prefixToPos, "/p ") {
				cmdPrefix = "/p "
			} else if strings.HasPrefix(prefixToPos, "/providers ") {
				cmdPrefix = "/providers "
			}
			filterPrefix := strings.TrimPrefix(prefixToPos, cmdPrefix)
			if strings.HasPrefix(filterPrefix, "select ") || strings.HasPrefix(filterPrefix, "use ") || strings.HasPrefix(filterPrefix, "remove ") || strings.HasPrefix(filterPrefix, "delete ") {
				var subCmd string
				if strings.HasPrefix(filterPrefix, "select ") {
					subCmd = "select "
				} else if strings.HasPrefix(filterPrefix, "use ") {
					subCmd = "use "
				} else if strings.HasPrefix(filterPrefix, "remove ") {
					subCmd = "remove "
				} else {
					subCmd = "delete "
				}
				provPrefix := strings.TrimPrefix(filterPrefix, subCmd)
				var providerKeys []string
				providerKeys = append(providerKeys, "default")
				if a != nil && a.Config != nil && a.Config.Providers != nil {
					for k := range a.Config.Providers {
						providerKeys = append(providerKeys, k)
					}
				}
				for _, pk := range providerKeys {
					if strings.HasPrefix(pk, provPrefix) {
						matches = append(matches, subCmd+pk)
					}
				}
			} else {
				// Suggest provider names directly for fast switching
				var providerKeys []string
				providerKeys = append(providerKeys, "default")
				if a != nil && a.Config != nil && a.Config.Providers != nil {
					for k := range a.Config.Providers {
						providerKeys = append(providerKeys, k)
					}
				}
				for _, pk := range providerKeys {
					if strings.HasPrefix(pk, filterPrefix) {
						matches = append(matches, pk)
					}
				}
				// And suggest subcommands
				for _, c := range providerSubcommands {
					if strings.HasPrefix(c, filterPrefix) {
						match := c
						if c == "add" || c == "select" || c == "use" || c == "model" || c == "timeout" || c == "remove" || c == "delete" {
							match += " "
						}
						matches = append(matches, match)
					}
				}
			}
		} else if strings.HasPrefix(prefixToPos, "/skills load ") && a != nil {
			filterPrefix := strings.TrimPrefix(prefixToPos, "/skills load ")
			for _, s := range a.ActiveSkills {
				if strings.HasPrefix(s.Name, filterPrefix) {
					matches = append(matches, s.Name)
				}
			}
		} else if strings.HasPrefix(prefixToPos, "/tools ") || strings.HasPrefix(prefixToPos, "/tool ") {
			cmdPrefix := "/tools "
			if strings.HasPrefix(prefixToPos, "/tool ") {
				cmdPrefix = "/tool "
			}
			filterPrefix := strings.TrimPrefix(prefixToPos, cmdPrefix)
			if strings.HasPrefix(filterPrefix, "disable ") || strings.HasPrefix(filterPrefix, "enable ") || strings.HasPrefix(filterPrefix, "toggle ") {
				var subCmd string
				if strings.HasPrefix(filterPrefix, "disable ") {
					subCmd = "disable "
				} else if strings.HasPrefix(filterPrefix, "enable ") {
					subCmd = "enable "
				} else {
					subCmd = "toggle "
				}
				toolPrefix := strings.TrimPrefix(filterPrefix, subCmd)
				if subCmd == "enable " && strings.HasPrefix("all", toolPrefix) {
					matches = append(matches, "all")
				}
				if a != nil && a.Registry != nil {
					executors := a.Registry.GetAllExecutors()
					var toolNames []string
					for name := range executors {
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
				toolSubcommands := []string{"list", "disable", "enable", "toggle", "reset", "menu"}
				for _, c := range toolSubcommands {
					if strings.HasPrefix(c, filterPrefix) {
						match := c
						if c == "disable" || c == "enable" || c == "toggle" {
							match += " "
						}
						matches = append(matches, match)
					}
				}
			}
		} else if strings.HasPrefix(prefixToPos, "/task ") || strings.HasPrefix(prefixToPos, "/stop ") || strings.HasPrefix(prefixToPos, "/kill ") || strings.HasPrefix(prefixToPos, "/status ") || strings.HasPrefix(prefixToPos, "/taskstop ") || strings.HasPrefix(prefixToPos, "/task_kill ") || strings.HasPrefix(prefixToPos, "/task_status ") {
			var cmdPrefix string
			for _, p := range []string{"/task ", "/stop ", "/kill ", "/status ", "/taskstop ", "/task_kill ", "/task_status "} {
				if strings.HasPrefix(prefixToPos, p) {
					cmdPrefix = p
					break
				}
			}
			filterPrefix := strings.TrimPrefix(prefixToPos, cmdPrefix)
			if cmdPrefix == "/task " {
				taskSubcommands := []string{"list", "view", "stream", "kill", "remove", "clear"}
				for _, sub := range []string{"view ", "stream ", "kill ", "remove "} {
					if strings.HasPrefix(filterPrefix, sub) {
						idPrefix := strings.TrimPrefix(filterPrefix, sub)
						if a != nil {
							for _, t := range a.ListTasks() {
								if strings.HasPrefix(t.ID, idPrefix) {
									matches = append(matches, sub+t.ID)
								}
							}
						}
					}
				}
				if len(matches) == 0 {
					for _, c := range taskSubcommands {
						if strings.HasPrefix(c, filterPrefix) {
							match := c
							if c != "list" && c != "clear" {
								match += " "
							}
							matches = append(matches, match)
						}
					}
					if a != nil {
						for _, t := range a.ListTasks() {
							if strings.HasPrefix(t.ID, filterPrefix) {
								matches = append(matches, t.ID)
							}
						}
					}
				}
			} else {
				if a != nil {
					for _, t := range a.ListTasks() {
						if strings.HasPrefix(t.ID, filterPrefix) {
							matches = append(matches, t.ID)
						}
					}
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

		entries, err := os.ReadDir(searchDir)
		if err == nil {
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
		newPos := wordStartIdx + len(matches[0])
		return completedLine, newPos, true
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
		newPos := wordStartIdx + len(commonPrefix)
		return completedLine, newPos, true
	}

	return line, pos, false
}
