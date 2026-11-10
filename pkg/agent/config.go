package agent

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/domain/limits"
	domaintool "loop/pkg/domain/tool"
)

// ConfigApplyError reports a configuration that was committed before a
// dependent service failed. Callers must finish synchronizing active state.
type ConfigApplyError struct{ Err error }

func (e *ConfigApplyError) Error() string { return "configuration applied, but " + e.Err.Error() }
func (e *ConfigApplyError) Unwrap() error { return e.Err }

// ConfigApplied distinguishes committed updates from preparation failures.
func ConfigApplied(err error) bool {
	var applied *ConfigApplyError
	return err == nil || errors.As(err, &applied)
}

// ApplyConfig prepares local resources before replacing the active configuration.
// Callers must keep inference idle while changing configuration.
func (a *Agent) ApplyConfig(input *config.Config) error {
	return a.applyConfig(input, configApplyOptions{})
}

// ApplyConfigAndReconnect commits settings and refreshes MCP in one idle operation.
func (a *Agent) ApplyConfigAndReconnect(input *config.Config) error {
	return a.applyConfig(input, configApplyOptions{reconnectMCP: true})
}

// ReloadConfigResources refreshes file-backed resources before committing their
// new configuration. Callers must reserve the owning session's idle guard.
func (a *Agent) ReloadConfigResources(input *config.Config) error {
	return a.applyConfig(input, configApplyOptions{reloadResources: true})
}

type configApplyOptions struct {
	reconnectMCP    bool
	reloadResources bool
}

func (a *Agent) applyConfig(input *config.Config, options configApplyOptions) error {
	if a == nil || a.Registry == nil {
		return fmt.Errorf("agent is unavailable")
	}
	next, err := config.Clone(input)
	if err != nil {
		return err
	}
	next.UpdateActiveProvider()
	if err := next.ValidateSettings(); err != nil {
		return err
	}
	if options.reconnectMCP {
		if !next.MCP {
			return fmt.Errorf("enable MCP before reconnecting")
		}
		enabled := false
		for _, server := range next.MCPServers {
			enabled = enabled || server.Enabled
		}
		if !enabled {
			return fmt.Errorf("enable at least one MCP server before reconnecting")
		}
	}
	next.NormalizeSafety()
	next.Limits = limits.Resolve(next.Limits)
	previous := a.Config
	client, err := a.httpClientForConfig(next)
	if err != nil {
		return err
	}
	plugins := tool.NewToolRegistry()
	pluginsChanged := options.reloadResources || previous == nil || previous.LocalPlugins != next.LocalPlugins || previous.ProjectResources != next.ProjectResources || !reflect.DeepEqual(previous.PluginDirectories, next.PluginDirectories)
	if pluginsChanged && next.LocalPlugins {
		for _, dir := range a.pluginDirsForConfig(next) {
			if err := tool.RegisterPluginsContext(a.Context(), plugins, dir); err != nil {
				return fmt.Errorf("load plugins: %w", err)
			}
		}
	}
	skillsChanged := options.reloadResources || previous == nil || previous.SkillsDir != next.SkillsDir || previous.ProjectResources != next.ProjectResources || !reflect.DeepEqual(previous.SkillDirectories, next.SkillDirectories)
	var skills []Skill
	if skillsChanged {
		skills, err = LoadSkillsFromDirs(a.skillDirsForConfig(next)...)
		if err != nil {
			return fmt.Errorf("load skills: %w", err)
		}
	}
	logger := a.DebugLogger
	loggerChanged := a.GetDebugLogPath() != ResolveDebugTarget(next.DebugLogFile)
	if loggerChanged {
		logger, err = openDebugLogger(next.DebugLogFile)
		if err != nil {
			return fmt.Errorf("open debug log: %w", err)
		}
	}
	committed := false
	defer func() {
		if !committed && loggerChanged && logger != nil {
			logger.Close()
		}
	}()
	catalogChanged := options.reloadResources || previous == nil || previous.PromptsFile != next.PromptsFile
	if catalogChanged || next.PromptsFile != "" {
		path := next.PromptsFile
		if path != "" {
			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("load prompt catalog: %w", err)
			}
		} else {
			path = domaintool.DefaultPromptCatalogPath()
		}
		if err := domaintool.LoadPromptCatalog(path); err != nil {
			return err
		}
	}
	mcpChanged := previous == nil || previous.MCP != next.MCP || !config.MCPServersEqual(previous.MCPServers, next.MCPServers) || tlsSettingsChanged(previous, next)
	a.LLMProviderMu.Lock()
	previousClient := a.HTTPClient
	a.HTTPClient = client
	a.Config = next
	if ui, ok := a.UI.(interface{ SetConfig(*config.Config) }); ok {
		ui.SetConfig(next)
	}
	a.ensureGuided().SetEnabled(next.GuidedMode)
	if a.FileObservations != nil {
		a.FileObservations.SetDisabled(!next.FileObservationGuard)
	}
	a.syncOpenAIProviderLocked(next, true)
	a.LLMProviderMu.Unlock()
	if previousClient != nil && previousClient != client {
		previousClient.CloseIdleConnections()
	}
	if pluginsChanged {
		a.Registry.ReplacePrefix("plugin__", plugins.GetAllExecutors())
	}
	if skillsChanged {
		a.SetActiveSkills(skills)
	}
	if loggerChanged {
		if a.DebugLogger != nil {
			a.DebugLogger.Close()
		}
		a.DebugLogger = logger
	}
	committed = true

	a.stateMu.Lock()
	a.emittedMemoryErrors = nil
	a.stateMu.Unlock()
	a.syncToolAvailability()
	a.markSystemPromptDirty()
	if mcpChanged || options.reconnectMCP {
		a.StopMCPServers()
		servers := config.CopyMCPServers(next.MCPServers)
		if options.reconnectMCP {
			for name, server := range servers {
				server.AlwaysLoad = true
				servers[name] = server
			}
		}
		if err := a.StartMCPServers(servers); err != nil {
			return &ConfigApplyError{Err: err}
		}
	}
	return nil
}

func tlsSettingsChanged(previous, next *config.Config) bool {
	return previous == nil || previous.TLSVerify != next.TLSVerify || previous.CAFile != next.CAFile || previous.CertFile != next.CertFile || previous.KeyFile != next.KeyFile
}

func (a *Agent) httpClientForConfig(next *config.Config) (*http.Client, error) {
	if a.HTTPClient != nil && !tlsSettingsChanged(a.Config, next) && next.CAFile == "" && next.CertFile == "" && next.KeyFile == "" {
		return a.HTTPClient, nil
	}
	tlsConfig, err := config.GetTLSConfig(next)
	if err != nil {
		return nil, err
	}
	previous := a.HTTPClient
	if previous == nil {
		previous = &http.Client{}
	}
	base := previous.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	transport, ok := base.(*http.Transport)
	if !ok {
		if tlsSettingsChanged(a.Config, next) {
			return nil, fmt.Errorf("cannot change TLS settings on a custom HTTP transport")
		}
		return previous, nil
	}
	nextTransport := transport.Clone()
	nextTransport.TLSClientConfig = tlsConfig
	client := *previous
	client.Transport = nextTransport
	return &client, nil
}
