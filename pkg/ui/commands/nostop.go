package commands

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/ui"
	"loop/pkg/ui/interactive"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

func handleNostopCommand(a *agent.Agent, parts []string, theme *style.UITheme, w io.Writer, reader *interceptor.KeyInterceptorReader) {
	if a == nil || a.Config == nil {
		return
	}
	usage := "usage: /nostop [eco|balanced|continuous <seconds> | off | status] (alias: /loop)"
	args := parts[1:]
	if len(args) == 0 || (len(args) == 1 && args[0] == "setup") {
		if reader == nil {
			fmt.Fprintln(w, usage)
			return
		}
		next, confirmed := interactive.RunNostopSetup(w, *theme)
		ui.RestoreAfterModal(w, a, reader, terminalFromReader(reader))
		if !confirmed {
			fmt.Fprintln(w, "nostop setup cancelled.")
			return
		}
		args = []string{next.Mode, strconv.Itoa(*next.IntervalSeconds)}
	}

	mode := strings.ToLower(strings.TrimSpace(args[0]))
	if mode == "status" && len(args) == 1 {
		printNostopStatus(w, a.Config.Nostop)
		return
	}
	seconds := 0
	if mode != "off" || len(args) != 1 {
		if len(args) != 2 || mode == "off" {
			fmt.Fprintln(w, usage)
			return
		}
		var err error
		seconds, err = strconv.Atoi(strings.TrimSpace(args[1]))
		if err != nil {
			fmt.Fprintln(w, "nostop pause must be a whole number of seconds.")
			return
		}
	}
	next, err := config.NewNostopConfig(mode, seconds)
	if err != nil {
		fmt.Fprintln(w, err)
		return
	}
	cfg, err := config.Clone(a.Config)
	if err != nil {
		fmt.Fprintf(w, "nostop configuration failed: %v\n", err)
		return
	}
	cfg.Nostop = next
	if err := a.ApplyConfig(cfg); err != nil {
		fmt.Fprintf(w, "nostop update failed: %v\n", err)
		return
	}
	saveConfigUpdate(a, w)
	printNostopStatus(w, next)
}

func printNostopStatus(w io.Writer, cfg config.NostopConfig) {
	if !cfg.Enabled() {
		fmt.Fprintln(w, "nostop: off")
		return
	}
	fmt.Fprintf(w, "nostop: %s, pause %s. Stops at the final reply. Ctrl+C cancels; /nostop off disables the mode.\n", cfg.Mode, cfg.Pause())
}
