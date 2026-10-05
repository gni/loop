package ui

import (
	"loop/pkg/config"
	"loop/pkg/ui/style"
)

type UITheme = style.UITheme

func GetConfiguredTheme(cfg *config.Config) UITheme {
	theme := style.GetTheme(cfg.Theme)
	if cfg.SyntaxTheme != "" && cfg.SyntaxTheme != "auto" {
		theme.ChromaStyle = cfg.SyntaxTheme
	}
	return theme
}
