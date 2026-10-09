package ui

import (
	"loop/pkg/config"
	"loop/pkg/ui/style"
)

type UITheme = style.UITheme

func GetConfiguredTheme(cfg *config.Config) UITheme {
	if cfg == nil {
		return style.ResolveConfiguredTheme("", "")
	}
	return style.ResolveConfiguredTheme(cfg.Theme, cfg.SyntaxTheme)
}
