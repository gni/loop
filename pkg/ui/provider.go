package ui

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/term"

	"maquis/pkg/agent"
	"maquis/pkg/config"
	"maquis/pkg/db"
	"maquis/pkg/ui/style"
)

func cloneProviderConfig(cfg *config.Config) *config.Config {
	if cfg == nil {
		return nil
	}

	cloned := *cfg
	cloned.Providers = make(map[string]config.ProviderConfig, len(cfg.Providers))
	for name, provider := range cfg.Providers {
		cloned.Providers[name] = provider
	}
	return &cloned
}

func commitProviderConfig(a *agent.Agent, next *config.Config) error {
	if a == nil || next == nil {
		return fmt.Errorf("provider configuration is unavailable")
	}
	if next.ActiveProvider == "" {
		if _, ok := next.Providers["default"]; ok {
			next.ActiveProvider = "default"
		}
	}
	if next.ActiveProvider != "" {
		if err := next.ActivateProvider(next.ActiveProvider); err != nil {
			return err
		}
	}
	if err := config.SaveConfig(a.ConfigPath, next); err != nil {
		return fmt.Errorf("save provider configuration: %w", err)
	}
	a.ApplyConfig(next)
	return nil
}

func HandleProviderCommand(
	a *agent.Agent,
	parts []string,
	messages *[]db.Message,
	theme UITheme,
	w io.Writer,
	kiReader *keyInterceptorReader,
) {
	calcHistoryTokens := func() (int, int, bool) {
		var mam *agent.MultiAgentManager
		if kiReader != nil {
			mam = kiReader.mam
		}
		var msgs []db.Message
		if messages != nil {
			msgs = *messages
		}
		return calculateActiveTokenUsage(a, msgs, activeToolAllowlist(kiReader), mam)
	}

	if len(parts) < 2 {
		input, output, cleanup := getInteractiveIO(kiReader)
		defer cleanup()

		ShutdownStatusBar(os.Stderr)
		newConfig, errInteractive := RunInteractiveProviderConfig(a.Config, theme, input, output, a.ConfigPath)
		InitStatusBar(os.Stderr)
		if errInteractive == nil && newConfig != nil {
			if err := commitProviderConfig(a, newConfig); err != nil {
				fmt.Fprintf(w, "error: %v\n", err)
			}
		} else if errInteractive != nil {
			fmt.Fprintf(w, "error: provider configuration failed: %v\n", errInteractive)
		}

		if kiReader != nil {
			redrawScreen(w, a, kiReader, kiReader.rl)
		} else {
			redrawScreen(w, a, nil, nil)
		}
		return
	}

	sub := parts[1]
	switch sub {
	case "list":
		listProviders(w, a.Config, theme)
	case "add":
		if len(parts) < 4 {
			fmt.Fprintln(w, "usage: /provider add <name> <endpoint> [api_key] [model]")
			return
		}
		name := parts[2]
		endpoint := parts[3]
		apiKey := ""
		model := ""
		if len(parts) > 4 {
			apiKey = parts[4]
		}
		if len(parts) > 5 {
			model = strings.Join(parts[5:], " ")
		}
		next := cloneProviderConfig(a.Config)
		if next.Providers == nil {
			next.Providers = make(map[string]config.ProviderConfig)
		}
		next.Providers[name] = config.ProviderConfig{
			Name:     name,
			Endpoint: endpoint,
			ApiKey:   apiKey,
			Model:    model,
		}
		if err := commitProviderConfig(a, next); err != nil {
			fmt.Fprintf(w, "error: %v\n", err)
			return
		}
		fmt.Fprintf(w, "Provider '%s' added successfully. To use it, run: /provider select %s (or /provider %s)\n", name, name, name)
	case "select", "use":
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /provider select <name>")
			return
		}
		name := parts[2]
		next := cloneProviderConfig(a.Config)
		if name == "none" {
			if _, ok := next.Providers["default"]; ok {
				name = "default"
			} else {
				name = ""
			}
		}
		if name != "" {
			if _, ok := next.Providers[name]; !ok {
				fmt.Fprintf(w, "error: provider '%s' not found.\n", name)
				return
			}
		}
		next.ActiveProvider = name
		if err := commitProviderConfig(a, next); err != nil {
			fmt.Fprintf(w, "error: %v\n", err)
			return
		}

		if name == "" || name == "default" {
			fmt.Fprintln(w, "Switched to default endpoint settings.")
		} else {
			fmt.Fprintf(w, "Switched active provider to '%s' (Endpoint: %s, Model: %s).\n", name, a.Config.Endpoint, a.Config.Model)
		}
		pTok, cTok, estimated := calcHistoryTokens()
		UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens, estimated)
		DrawStatusBar(os.Stderr, theme)
	case "model":
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /provider model <model>")
			return
		}
		model := strings.Join(parts[2:], " ")
		next := cloneProviderConfig(a.Config)
		next.Model = model
		next.UpdateActiveProvider()
		if err := commitProviderConfig(a, next); err != nil {
			fmt.Fprintf(w, "error: %v\n", err)
			return
		}
		if a.Config.ActiveProvider != "" {
			fmt.Fprintf(w, "Updated model for provider '%s' to '%s'.\n", a.Config.ActiveProvider, model)
		} else {
			fmt.Fprintf(w, "Updated model to '%s'.\n", model)
		}
		pTok, cTok, estimated := calcHistoryTokens()
		UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens, estimated)
		DrawStatusBar(os.Stderr, theme)
	case "timeout":
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /provider timeout <seconds>")
			return
		}
		sec, err := strconv.Atoi(parts[2])
		if err != nil || sec < 0 {
			fmt.Fprintf(w, "error: timeout must be a non-negative integer\n")
			return
		}
		next := cloneProviderConfig(a.Config)
		next.Timeout = sec
		next.UpdateActiveProvider()
		if err := commitProviderConfig(a, next); err != nil {
			fmt.Fprintf(w, "error: %v\n", err)
			return
		}
		if a.Config.ActiveProvider != "" {
			fmt.Fprintf(w, "Updated timeout for provider '%s' to %ds.\n", a.Config.ActiveProvider, sec)
		} else {
			fmt.Fprintf(w, "Updated default timeout to %ds.\n", sec)
		}
		pTok, cTok, estimated := calcHistoryTokens()
		UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens, estimated)
		DrawStatusBar(os.Stderr, theme)
	case "remove", "delete":
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /provider remove <name>")
			return
		}
		name := parts[2]
		next := cloneProviderConfig(a.Config)
		if _, ok := next.Providers[name]; !ok {
			fmt.Fprintf(w, "error: provider '%s' not found.\n", name)
			return
		}
		delete(next.Providers, name)
		if next.ActiveProvider == name {
			next.ActiveProvider = ""
			if name != "default" {
				if _, ok := next.Providers["default"]; ok {
					next.ActiveProvider = "default"
				}
			}
		}
		if err := commitProviderConfig(a, next); err != nil {
			fmt.Fprintf(w, "error: %v\n", err)
			return
		}
		fmt.Fprintf(w, "Provider '%s' removed.\n", name)
		pTok, cTok, estimated := calcHistoryTokens()
		UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens, estimated)
		DrawStatusBar(os.Stderr, theme)
	default:
		// Direct provider switch shortcut: /provider <name> [optional-model]
		if _, ok := a.Config.Providers[sub]; ok || sub == "default" || sub == "none" {
			name := sub
			next := cloneProviderConfig(a.Config)
			if name == "none" {
				if _, ok := next.Providers["default"]; ok {
					name = "default"
				} else {
					name = ""
				}
			}
			next.ActiveProvider = name
			if len(parts) > 2 {
				next.Model = strings.Join(parts[2:], " ")
				next.UpdateActiveProvider()
			}
			if err := commitProviderConfig(a, next); err != nil {
				fmt.Fprintf(w, "error: %v\n", err)
				return
			}

			if name == "" || name == "default" {
				fmt.Fprintln(w, "Switched to default endpoint settings.")
			} else {
				fmt.Fprintf(w, "Switched active provider to '%s' (Endpoint: %s, Model: %s).\n", name, a.Config.Endpoint, a.Config.Model)
			}
			pTok, cTok, estimated := calcHistoryTokens()
			UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens, estimated)
			DrawStatusBar(os.Stderr, theme)
			return
		}

		fmt.Fprintf(w, "unknown subcommand or provider '%s'.\n", sub)
		printProviderHelp(w, theme)
	}
}


func listProviders(w io.Writer, cfg *config.Config, theme UITheme) {
	if cfg.Providers == nil || len(cfg.Providers) == 0 {
		fmt.Fprintln(w, "No custom endpoint providers configured.")
		return
	}

	fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("Configured Endpoint Providers:"))

	var keys []string
	for k := range cfg.Providers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, name := range keys {
		item := cfg.Providers[name]
		marker := "  "
		if name == cfg.ActiveProvider {
			marker = style.NewStyle().Foreground(theme.Success).Render("➔ ")
		}

		apiKeyDisplay := "none"
		if item.ApiKey != "" {
			if strings.HasPrefix(item.ApiKey, "$") || strings.HasPrefix(item.ApiKey, "env:") {
				apiKeyDisplay = item.ApiKey
			} else {
				apiKeyDisplay = "configured"
			}
		}
		timeoutDisplay := ""
		if item.Timeout > 0 {
			timeoutDisplay = fmt.Sprintf(" | Timeout: %ds", item.Timeout)
		}

		fmt.Fprintf(w, " %s %-12s : URL: %s | Model: %s | API Key: %s%s\n",
			marker,
			style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(name),
			item.Endpoint,
			item.Model,
			apiKeyDisplay,
			timeoutDisplay,
		)
	}
	if cfg.ActiveProvider == "" {
		defaultTimeoutDisplay := ""
		if cfg.Timeout > 0 {
			defaultTimeoutDisplay = fmt.Sprintf(" | Timeout: %ds", cfg.Timeout)
		}
		fmt.Fprintf(w, " %s %-12s : URL: %s | Model: %s%s | (Currently active default settings)\n",
			style.NewStyle().Foreground(theme.Success).Render("➔ "),
			style.NewStyle().Foreground(theme.Secondary).Bold(true).Render("default"),
			cfg.Endpoint,
			cfg.Model,
			defaultTimeoutDisplay,
		)
	}
}

func printProviderHelp(w io.Writer, theme UITheme) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("Usage:"))
	fmt.Fprintln(w, "  /provider                                  Open custom interactive provider configuration page")
	fmt.Fprintln(w, "  /provider <name> [model]                   Directly switch active provider (or /p <name>)")
	fmt.Fprintln(w, "  /provider list                             List configured providers")
	fmt.Fprintln(w, "  /provider add <name> <url> [key] [model]   Add a new provider profile")
	fmt.Fprintln(w, "  /provider select <name>                    Select active provider (use 'default' to reset)")
	fmt.Fprintln(w, "  /provider model <model>                    Set model for currently active provider")
	fmt.Fprintln(w, "  /provider timeout <seconds>                Set timeout for currently active provider")
	fmt.Fprintln(w, "  /provider remove <name>                    Remove a provider profile")
}

func RunInteractiveProviderConfig(
	cfg *config.Config,
	theme UITheme,
	rlInput io.Reader,
	rlOutput io.Writer,
	configPath string,
) (*config.Config, error) {
	var fd int
	if f, ok := rlInput.(*os.File); ok {
		fd = int(f.Fd())
	} else {
		fd = int(os.Stdin.Fd())
	}

	if !term.IsTerminal(fd) {
		return cfg, nil
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	defer term.Restore(fd, oldState)

	EnterAlternateScreen(rlOutput)
	fmt.Fprint(rlOutput, "\x1b[?25l")
	defer func() {
		fmt.Fprint(rlOutput, "\x1b[?25h")
		ExitAlternateScreen(rlOutput)
	}()

	clonedConfig := cloneProviderConfig(cfg)
	if clonedConfig == nil {
		return nil, fmt.Errorf("provider configuration is unavailable")
	}
	cloned := *clonedConfig
	if cloned.Providers == nil {
		cloned.Providers = make(map[string]config.ProviderConfig)
	}


	itemsProvider := func() []*settingItem {
		var items []*settingItem

		var pNames []string
		for p := range cloned.Providers {
			pNames = append(pNames, p)
		}
		sort.Strings(pNames)

		for _, pn := range pNames {
			pName := pn

			// 1. Provider header / activate toggle
			items = append(items, &settingItem{
				id:   "prov_act_" + pName,
				name: "provider: " + pName,
				value: func() string {
					if cloned.ActiveProvider == pName {
						return "➔ ACTIVE"
					}
					return "select"
				},
				description: fmt.Sprintf("Press Enter to set '%s' as the active provider", pName),
				onToggle: func() {
					cloned.ActiveProvider = pName
				},
			})

			// 2. Model
			items = append(items, &settingItem{
				id:   "prov_model_" + pName,
				name: "  model",
				value: func() string {
					curr := cloned.Providers[pName]
					if curr.Model != "" {
						return curr.Model
					}
					return "(not set)"
				},
				description: fmt.Sprintf("Model identifier to request for '%s'", pName),
				onEdit: func(newVal string) error {
					curr := cloned.Providers[pName]
					curr.Model = strings.TrimSpace(newVal)
					cloned.Providers[pName] = curr
					return nil
				},
			})

			// 3. Endpoint URL
			items = append(items, &settingItem{
				id:   "prov_endpoint_" + pName,
				name: "  endpoint",
				value: func() string {
					return cloned.Providers[pName].Endpoint
				},
				description: fmt.Sprintf("Base API endpoint URL for '%s'", pName),
				onEdit: func(newVal string) error {
					newVal = strings.TrimSpace(newVal)
					if newVal == "" {
						return fmt.Errorf("endpoint URL cannot be empty")
					}
					curr := cloned.Providers[pName]
					curr.Endpoint = newVal
					cloned.Providers[pName] = curr
					return nil
				},
			})

			// 4. API Key
			items = append(items, &settingItem{
				id:   "prov_key_" + pName,
				name: "  api key",
				value: func() string {
					key := cloned.Providers[pName].ApiKey
					if key == "" {
						return "(not set)"
					}
					if strings.HasPrefix(key, "$") || strings.HasPrefix(key, "env:") {
						return key
					}
					if len(key) <= 8 {
						return "********"
					}
					return key[:4] + "..." + key[len(key)-4:] + " (masked)"
				},
				description: fmt.Sprintf("API key or env reference ($VAR) for '%s'", pName),
				onEdit: func(newVal string) error {
					curr := cloned.Providers[pName]
					curr.ApiKey = strings.TrimSpace(newVal)
					cloned.Providers[pName] = curr
					return nil
				},
			})

			// 5. Timeout
			items = append(items, &settingItem{
				id:   "prov_timeout_" + pName,
				name: "  timeout (s)",
				value: func() string {
					if cloned.Providers[pName].Timeout > 0 {
						return fmt.Sprintf("%ds", cloned.Providers[pName].Timeout)
					}
					return "default"
				},
				description: fmt.Sprintf("Timeout in seconds for '%s' (0 to use default)", pName),
				onEdit: func(newVal string) error {
					newVal = strings.TrimSpace(newVal)
					if newVal == "" {
						return nil
					}
					sec, err := strconv.Atoi(newVal)
					if err != nil || sec < 0 {
						return fmt.Errorf("must be a non-negative integer")
					}
					curr := cloned.Providers[pName]
					curr.Timeout = sec
					cloned.Providers[pName] = curr
					return nil
				},
			})

			// 6. Delete provider
			items = append(items, &settingItem{
				id:   "prov_del_" + pName,
				name: "  [ delete " + pName + " ]",
				value: func() string {
					return ""
				},
				description: fmt.Sprintf("Remove the provider '%s'", pName),
				onToggle: func() {
					delete(cloned.Providers, pName)
					if cloned.ActiveProvider == pName {
						cloned.ActiveProvider = ""
						if _, ok := cloned.Providers["default"]; ok {
							cloned.ActiveProvider = "default"
						}
					}
				},
			})
		}

		// Action: Add New Provider
		items = append(items, &settingItem{
			id:          "action_add",
			name:        "[ add new provider ]",
			value:       func() string { return "" },
			description: "Configure a new endpoint provider profile",
			onToggle: func() {
				fmt.Fprint(rlOutput, "\r\n\r\n  === Add New Endpoint Provider ===\r\n")
				fmt.Fprint(rlOutput, "  Provider name (e.g. brain, ollama, groq): ")
				pName, err := readInputRaw(rlInput, rlOutput)
				if err != nil {
					return
				}
				pName = strings.TrimSpace(pName)
				if pName == "" {
					return
				}
				fmt.Fprint(rlOutput, "  Base endpoint URL: ")
				pURL, err := readInputRaw(rlInput, rlOutput)
				if err != nil {
					return
				}
				pURL = strings.TrimSpace(pURL)
				if pURL == "" {
					return
				}
				fmt.Fprint(rlOutput, "  Model identifier (optional): ")
				pModel, _ := readInputRaw(rlInput, rlOutput)
				pModel = strings.TrimSpace(pModel)

				fmt.Fprint(rlOutput, "  API key or $ENV_VAR (optional): ")
				pKey, _ := readInputRaw(rlInput, rlOutput)
				pKey = strings.TrimSpace(pKey)

				if cloned.Providers == nil {
					cloned.Providers = make(map[string]config.ProviderConfig)
				}
				cloned.Providers[pName] = config.ProviderConfig{
					Name:     pName,
					Endpoint: pURL,
					Model:    pModel,
					ApiKey:   pKey,
					Timeout:  cloned.Timeout,
				}
				cloned.ActiveProvider = pName
			},
		})

		return items
	}

	extraRender := func(buf *strings.Builder) {
		if len(cloned.Providers) == 0 {
			return
		}
		buf.WriteString("\n  ")
		buf.WriteString(style.NewStyle().Foreground(theme.Primary).Bold(true).Render("provider overview:"))
		buf.WriteString("\n")

		var keys []string
		for k := range cloned.Providers {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, name := range keys {
			p := cloned.Providers[name]
			marker := "   "
			if name == cloned.ActiveProvider {
				marker = " ➔ "
			}

			nameStyled := style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(fmt.Sprintf("%-12s", name))
			if name == cloned.ActiveProvider {
				nameStyled = style.NewStyle().Foreground(theme.Success).Bold(true).Render(fmt.Sprintf("%-12s", name))
			}

			modelStr := p.Model
			if modelStr == "" {
				modelStr = "default"
			}
			if len(modelStr) > 20 {
				modelStr = modelStr[:17] + "..."
			}

			timeoutStr := ""
			if p.Timeout > 0 {
				timeoutStr = fmt.Sprintf(" (%ds)", p.Timeout)
			}

			buf.WriteString(fmt.Sprintf("  %s%s model: %-20s  url: %s%s\n",
				marker,
				nameStyled,
				modelStr,
				p.Endpoint,
				timeoutStr,
			))
		}
	}

	err = runSettingsMenuLoop(rlInput, rlOutput, theme, "endpoint providers config", itemsProvider, extraRender)
	if err != nil {
		return nil, err
	}
	if cloned.ActiveProvider != "" {
		if err := cloned.ActivateProvider(cloned.ActiveProvider); err != nil {
			return nil, err
		}
	}
	return &cloned, nil
}
