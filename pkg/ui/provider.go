package ui

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/interactive"
	"loop/pkg/ui/style"
)

func cloneProviderConfig(cfg *config.Config) *config.Config {
	return interactive.CloneProviderConfig(cfg)
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
		var mam *swarm.MultiAgentManager
		if kiReader != nil {
			mam = kiReader.MAM
		}
		var msgs []db.Message
		if messages != nil {
			msgs = *messages
		}
		return calculateActiveTokenUsage(a, msgs, activeToolAllowlist(kiReader), mam)
	}

	syncStatus := func() {
		syncAgentStatus(a, theme, calcHistoryTokens)
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
			redrawScreen(w, a, kiReader, kiReader.RL)
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

		printProviderSwitchMessage(w, name, a.Config.Endpoint, a.Config.Model)
		syncStatus()
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
		syncStatus()
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
		syncStatus()
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
		syncStatus()
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

			printProviderSwitchMessage(w, name, a.Config.Endpoint, a.Config.Model)
			syncStatus()
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

func printProviderSwitchMessage(w io.Writer, name, endpoint, model string) {
	if name == "" || name == "default" {
		fmt.Fprintln(w, "Switched to default endpoint settings.")
	} else {
		fmt.Fprintf(w, "Switched active provider to '%s' (Endpoint: %s, Model: %s).\n", name, endpoint, model)
	}
}

