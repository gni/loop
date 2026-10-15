package interactive

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"loop/pkg/config"
	"loop/pkg/ui/style"
)

func CloneProviderConfig(cfg *config.Config) *config.Config {
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

func RunInteractiveProviderConfig(
	cfg *config.Config,
	theme style.UITheme,
	rlInput io.Reader,
	rlOutput io.Writer,
	configPath string,
) (*config.Config, error) {
	clonedConfig := CloneProviderConfig(cfg)
	if clonedConfig == nil {
		return nil, fmt.Errorf("provider configuration is unavailable")
	}
	cloned := *clonedConfig
	if cloned.Providers == nil {
		cloned.Providers = make(map[string]config.ProviderConfig)
	}

	isTTY, err := withAltScreenTerminal(rlInput, rlOutput, func() error {
		itemsProvider := func() []*settingItem {
			var items []*settingItem
			var pNames []string
			for p := range cloned.Providers {
				pNames = append(pNames, p)
			}
			sort.Strings(pNames)

			for _, pn := range pNames {
				pName := pn
				items = append(items,
					&settingItem{
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
					},
					&settingItem{
						id:   "prov_model_" + pName,
						name: "  model",
						value: func() string {
							if curr := cloned.Providers[pName]; curr.Model != "" {
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
					},
					&settingItem{
						id:          "prov_endpoint_" + pName,
						name:        "  endpoint",
						value:       func() string { return cloned.Providers[pName].Endpoint },
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
					},
					&settingItem{
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
					},
					&settingItem{
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
					},
					&settingItem{
						id:          "prov_del_" + pName,
						name:        "  [ delete " + pName + " ]",
						value:       func() string { return "" },
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
					},
				)
			}

			items = append(items, &settingItem{
				id:          "action_add",
				name:        "[ add new provider ]",
				value:       func() string { return "" },
				description: "Configure a new endpoint provider profile",
				onToggle: func() {
					fmt.Fprint(rlOutput, "\r\n\r\n  === Add New Endpoint Provider ===\r\n")
					fmt.Fprint(rlOutput, "  Provider name (e.g. brain, ollama, groq): ")
					pName, err := ReadInputRaw(rlInput, rlOutput)
					if err != nil {
						return
					}
					pName = strings.TrimSpace(pName)
					if pName == "" {
						return
					}
					fmt.Fprint(rlOutput, "  Base endpoint URL: ")
					pURL, err := ReadInputRaw(rlInput, rlOutput)
					if err != nil {
						return
					}
					pURL = strings.TrimSpace(pURL)
					if pURL == "" {
						return
					}
					fmt.Fprint(rlOutput, "  Model identifier (optional): ")
					pModel, _ := ReadInputRaw(rlInput, rlOutput)
					pModel = strings.TrimSpace(pModel)

					fmt.Fprint(rlOutput, "  API key or $ENV_VAR (optional): ")
					pKey, _ := ReadInputRaw(rlInput, rlOutput)
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
			buf.WriteString("\n  " + style.NewStyle().Foreground(theme.Primary).Bold(true).Render("provider overview:") + "\n")

			var keys []string
			for k := range cloned.Providers {
				keys = append(keys, k)
			}
			sort.Strings(keys)

			for _, name := range keys {
				p := cloned.Providers[name]
				marker := "   "
				nameColor := theme.Secondary
				if name == cloned.ActiveProvider {
					marker = " ➔ "
					nameColor = theme.Success
				}
				nameStyled := style.NewStyle().Foreground(nameColor).Bold(true).Render(fmt.Sprintf("%-12s", name))

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

				buf.WriteString(fmt.Sprintf("  %s%s model: %-20s  url: %s%s\n", marker, nameStyled, modelStr, p.Endpoint, timeoutStr))
			}
		}

		return runSettingsMenuLoop(rlInput, rlOutput, theme, "endpoint providers config", itemsProvider, extraRender)
	})
	if !isTTY {
		return cfg, nil
	}
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