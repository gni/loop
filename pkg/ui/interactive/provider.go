package interactive

import (
	"fmt"
	"io"
	"maps"
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
	cloned, err := config.Clone(cfg)
	if err != nil || cloned == nil {
		fallback := *cfg
		fallback.Providers = make(map[string]config.ProviderConfig, len(cfg.Providers))
		maps.Copy(fallback.Providers, cfg.Providers)
		return &fallback
	}
	if cloned.Providers == nil {
		cloned.Providers = make(map[string]config.ProviderConfig)
	}
	return cloned
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

	isTTY, err := WithAltScreenTerminal(rlInput, rlOutput, func() error {
		sr := NewSessionReader(rlInput)
		defer sr.Close()

		itemsProvider := func() []*SettingItem {
			var items []*SettingItem
			var pNames []string
			for p := range cloned.Providers {
				pNames = append(pNames, p)
			}
			sort.Strings(pNames)

			for _, pn := range pNames {
				pName := pn
				items = append(items,
					&SettingItem{
						ID:   "prov_act_" + pName,
						Name: "provider: " + pName,
						Value: func() string {
							if cloned.ActiveProvider == pName {
								return "➔ ACTIVE"
							}
							return "select"
						},
						Description: fmt.Sprintf("Press Enter to set '%s' as the active provider", pName),
						OnToggle: func() {
							cloned.ActiveProvider = pName
						},
					},
					&SettingItem{
						ID:   "prov_model_" + pName,
						Name: "  model",
						Value: func() string {
							if curr := cloned.Providers[pName]; curr.Model != "" {
								return curr.Model
							}
							return "(not set)"
						},
						Description: fmt.Sprintf("Model identifier to request for '%s'", pName),
						OnEdit: func(newVal string) error {
							curr := cloned.Providers[pName]
							curr.Model = strings.TrimSpace(newVal)
							cloned.Providers[pName] = curr
							return nil
						},
					},
					&SettingItem{
						ID:          "prov_endpoint_" + pName,
						Name:        "  endpoint",
						Value:       func() string { return cloned.Providers[pName].Endpoint },
						Description: fmt.Sprintf("Base API endpoint URL for '%s'", pName),
						OnEdit: func(newVal string) error {
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
					&SettingItem{
						ID:   "prov_key_" + pName,
						Name: "  api key",
						Value: func() string {
							key := cloned.Providers[pName].APIKey
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
						Description: fmt.Sprintf("API key or env reference ($VAR) for '%s'", pName),
						OnEdit: func(newVal string) error {
							curr := cloned.Providers[pName]
							curr.APIKey = strings.TrimSpace(newVal)
							cloned.Providers[pName] = curr
							return nil
						},
					},
					&SettingItem{
						ID:   "prov_timeout_" + pName,
						Name: "  timeout (s)",
						Value: func() string {
							if cloned.Providers[pName].Timeout > 0 {
								return fmt.Sprintf("%ds", cloned.Providers[pName].Timeout)
							}
							return "default"
						},
						Description: fmt.Sprintf("Timeout in seconds for '%s' (0 to use default)", pName),
						OnEdit: func(newVal string) error {
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
					&SettingItem{
						ID:          "prov_del_" + pName,
						Name:        "  [ delete " + pName + " ]",
						Value:       func() string { return "" },
						Description: fmt.Sprintf("Remove the provider '%s'", pName),
						OnToggle: func() {
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

			items = append(items, &SettingItem{
				ID:          "action_add",
				Name:        "[ add new provider ]",
				Value:       func() string { return "" },
				Description: "Configure a new endpoint provider profile",
				OnToggle: func() {
					fmt.Fprint(rlOutput, "\r\n\r\n  === Add New Endpoint Provider ===\r\n")
					fmt.Fprint(rlOutput, "  Provider name (e.g. brain, ollama, groq): ")
					pName, err := ReadInputRaw(sr, rlOutput)
					if err != nil {
						return
					}
					pName = strings.TrimSpace(pName)
					if pName == "" {
						return
					}
					if _, exists := cloned.Providers[pName]; exists {
						fmt.Fprint(rlOutput, "  Provider already exists. Press enter to continue...")
						_, _ = ReadInputRaw(sr, rlOutput)
						return
					}
					fmt.Fprint(rlOutput, "  Base endpoint URL: ")
					pURL, err := ReadInputRaw(sr, rlOutput)
					if err != nil {
						return
					}
					pURL = strings.TrimSpace(pURL)
					if pURL == "" {
						return
					}
					fmt.Fprint(rlOutput, "  Model identifier (optional): ")
					pModel, err := ReadInputRaw(sr, rlOutput)
					if err != nil {
						return
					}
					pModel = strings.TrimSpace(pModel)

					fmt.Fprint(rlOutput, "  API key or $ENV_VAR (optional): ")
					pKey, err := ReadInputRaw(sr, rlOutput)
					if err != nil {
						return
					}
					pKey = strings.TrimSpace(pKey)

					if cloned.Providers == nil {
						cloned.Providers = make(map[string]config.ProviderConfig)
					}
					cloned.Providers[pName] = config.ProviderConfig{
						Name:     pName,
						Endpoint: pURL,
						Model:    pModel,
						APIKey:   pKey,
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

		return RunSettingsMenuLoop(sr, rlOutput, theme, "endpoint providers config", itemsProvider, extraRender)
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
