package history

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"loop/pkg/config"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/resources"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

const (
	slashMenuMaxVisible = 8
	slashExtraCacheTTL  = time.Second
)

func (h *ChatHistory) setSlashMenuOpenLocked(open bool) bool {
	changed := h.slashMenuOpen != open
	h.slashMenuOpen = open
	h.slashMenuOpenAtomic.Store(open)
	ui.GetUI().SetSlashMenuOpen(open)
	if !open {
		h.slashMenuHeaderRow = 0
		h.slashMenuStartRow = 0
		h.slashMenuEndRow = 0
		h.slashVisibleStart = 0
	}
	return changed
}

func (h *ChatHistory) redrawPromptSeparatorLocked() {
	if h.a == nil || h.reader == nil || h.reader.W == nil {
		return
	}
	live := h.a.LiveSettings()
	theme := style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
	ui.DrawStaticPromptSeparatorLocked(h.reader.W, live.ShowThinking, live.ReasoningEffort, theme)
}

func (h *ChatHistory) loadExtraTopSlashItems() []render.SlashCommandItem {
	now := time.Now()
	ui.TerminalMu.Lock()
	if now.Sub(h.slashExtraFetched) < slashExtraCacheTTL && h.slashExtraLoaded {
		cached := make([]render.SlashCommandItem, len(h.slashExtraCache))
		copy(cached, h.slashExtraCache)
		ui.TerminalMu.Unlock()
		return cached
	}
	ui.TerminalMu.Unlock()

	var extras []render.SlashCommandItem
	seen := make(map[string]bool)

	for _, dir := range h.promptTemplateDirs() {
		for _, tpl := range domaintool.LoadPromptTemplates(dir) {
			name := "/" + tpl.Name
			key := strings.ToLower(name)
			if tpl.Name == "" || seen[key] {
				continue
			}
			seen[key] = true
			desc := strings.TrimSpace(tpl.Description)
			if desc == "" {
				desc = "prompt template"
			}
			extras = append(extras, render.SlashCommandItem{
				Name:        name,
				Description: desc,
			})
		}
	}
	for _, ext := range ui.ListAgentExtensions(h.a) {
		key := strings.ToLower(ext.Name)
		if ext.Name == "" || seen[key] {
			continue
		}
		seen[key] = true
		desc := "custom extension"
		if ext.Location != "" {
			desc = ext.Location + " extension"
		}
		extras = append(extras, render.SlashCommandItem{
			Name:        ext.Name,
			Description: desc,
		})
	}
	sort.Slice(extras, func(i, j int) bool {
		return extras[i].Name < extras[j].Name
	})

	ui.TerminalMu.Lock()
	h.slashExtraCache = extras
	h.slashExtraFetched = now
	h.slashExtraLoaded = true
	ui.TerminalMu.Unlock()
	return extras
}

func (h *ChatHistory) promptTemplateDirs() []string {
	defaultDir := func() []string {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return []string{filepath.Join(home, ".loop", "prompts")}
		}
		return nil
	}
	if h.a == nil {
		return defaultDir()
	}
	h.a.LLMProviderMu.RLock()
	cfg := h.a.Config
	configPath := h.a.ConfigPath
	h.a.LLMProviderMu.RUnlock()

	includeProject := cfg == nil || (cfg.ProjectResources && cfg.LocalPlugins)
	var extra []string
	if cfg != nil {
		for _, dir := range cfg.PromptDirectories {
			if h.a.ProjectResourceAllowed(dir) {
				extra = append(extra, dir)
			}
		}
	}
	dirs := resources.PromptDirectories(resources.AgentDirectory(configPath), h.a.GetWorkspaceRoot(), includeProject, extra)
	if len(dirs) == 0 {
		return defaultDir()
	}
	return dirs
}

func (h *ChatHistory) dynamicSubcommands(query string) []render.SlashCommandItem {
	lower := strings.ToLower(query)
	switch {
	case strings.HasPrefix(lower, "/config set "):
		keys := config.SettingKeys()
		items := make([]render.SlashCommandItem, 0, len(keys))
		for _, k := range keys {
			items = append(items, render.SlashCommandItem{
				Name:        "/config set " + k,
				Args:        "<val>",
				Description: "set " + k,
				TakesArgs:   true,
			})
		}
		return items
	case strings.HasPrefix(lower, "/config "):
		keys := config.SettingKeys()
		items := make([]render.SlashCommandItem, 0, len(keys))
		for _, k := range keys {
			items = append(items, render.SlashCommandItem{
				Name:        "/config " + k,
				Args:        "<val>",
				Description: "set " + k,
				TakesArgs:   true,
			})
		}
		return items
	case (strings.HasPrefix(lower, "/skills load ") || strings.HasPrefix(lower, "/skill load ")) && h.a != nil:
		prefix := "/skills load "
		if strings.HasPrefix(lower, "/skill load ") {
			prefix = "/skill load "
		}
		skills := h.a.GetActiveSkills()
		items := make([]render.SlashCommandItem, 0, len(skills))
		for _, s := range skills {
			desc := strings.TrimSpace(s.Description)
			if desc == "" {
				desc = "load skill"
			}
			items = append(items, render.SlashCommandItem{
				Name:        prefix + s.Name,
				Description: desc,
			})
		}
		return items
	case (strings.HasPrefix(lower, "/provider ") || strings.HasPrefix(lower, "/providers ") || strings.HasPrefix(lower, "/p ")) && h.a != nil:
		spaceIdx := strings.IndexByte(query, ' ')
		cmdPrefix := query[:spaceIdx+1]
		rest := strings.ToLower(query[spaceIdx+1:])
		var providerKeys []string
		h.a.LLMProviderMu.RLock()
		if h.a.Config != nil {
			for k := range h.a.Config.Providers {
				providerKeys = append(providerKeys, k)
			}
		}
		h.a.LLMProviderMu.RUnlock()
		sort.Strings(providerKeys)
		providerKeys = append([]string{"default"}, providerKeys...)
		for _, sub := range []string{"select ", "use ", "remove ", "delete "} {
			if strings.HasPrefix(rest, sub) {
				items := make([]render.SlashCommandItem, 0, len(providerKeys))
				for _, pk := range providerKeys {
					items = append(items, render.SlashCommandItem{
						Name:        cmdPrefix + sub + pk,
						Description: "provider profile",
					})
				}
				return items
			}
		}
		items := make([]render.SlashCommandItem, 0, len(providerKeys))
		for _, pk := range providerKeys {
			items = append(items, render.SlashCommandItem{
				Name:        cmdPrefix + pk,
				Description: "switch to provider profile",
			})
		}
		return items
	case (strings.HasPrefix(lower, "/tools ") || strings.HasPrefix(lower, "/tool ")) && h.a != nil && h.a.Registry != nil:
		spaceIdx := strings.IndexByte(query, ' ')
		cmdPrefix := query[:spaceIdx+1]
		rest := strings.ToLower(query[spaceIdx+1:])
		for _, sub := range []string{"disable ", "enable ", "toggle "} {
			if !strings.HasPrefix(rest, sub) {
				continue
			}
			var names []string
			for name := range h.a.Registry.GetAllExecutors() {
				names = append(names, name)
			}
			sort.Strings(names)
			items := make([]render.SlashCommandItem, 0, len(names)+1)
			if sub == "enable " {
				items = append(items, render.SlashCommandItem{
					Name:        cmdPrefix + sub + "all",
					Description: "enable all tools",
				})
			}
			for _, name := range names {
				items = append(items, render.SlashCommandItem{
					Name:        cmdPrefix + sub + name,
					Description: strings.TrimSpace(sub) + " tool",
				})
			}
			return items
		}
	case (strings.HasPrefix(lower, "/task ") || strings.HasPrefix(lower, "/tasks ")) && h.a != nil:
		spaceIdx := strings.IndexByte(query, ' ')
		cmdPrefix := query[:spaceIdx+1]
		rest := strings.ToLower(query[spaceIdx+1:])
		tasks := h.a.ListTasks()
		if len(tasks) == 0 {
			return nil
		}
		for _, sub := range []string{"view ", "stream ", "kill ", "remove "} {
			if strings.HasPrefix(rest, sub) {
				items := make([]render.SlashCommandItem, 0, len(tasks))
				for _, t := range tasks {
					desc := strings.TrimSpace(t.Command)
					if desc == "" {
						desc = t.Status
					}
					items = append(items, render.SlashCommandItem{
						Name:        cmdPrefix + sub + t.ID,
						Description: desc,
					})
				}
				return items
			}
		}
	}
	return nil
}

// SlashMenuActive reports whether the inline slash-command menu is currently open and interactive.
func (h *ChatHistory) SlashMenuActive() bool {
	if h == nil || h.closed.Load() {
		return false
	}
	return h.slashMenuOpenAtomic.Load()
}

// SyncSlashMenu updates the live slash-command dropdown for the current prompt draft.
// It marks the viewport dirty so the prompt draw cycle repaints everything atomically.
func (h *ChatHistory) SyncSlashMenu(draft string) {
	if h == nil || h.closed.Load() || h.isNativeScroll() {
		return
	}
	isSlashDraft := strings.HasPrefix(draft, "/") && !strings.ContainsAny(draft, "\r\n")
	if !isSlashDraft && !h.slashMenuOpenAtomic.Load() {
		ui.TerminalMu.Lock()
		if h.slashDismissedQuery != "" {
			h.slashDismissedQuery = ""
		}
		ui.TerminalMu.Unlock()
		return
	}

	var extras []render.SlashCommandItem
	if isSlashDraft && !strings.Contains(draft, " ") {
		extras = h.loadExtraTopSlashItems()
	}

	var items []render.SlashCommandItem
	var title string
	if isSlashDraft {
		items, title = render.FilterSlashCommands(draft, extras, h.dynamicSubcommands)
	}

	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()

	if !isSlashDraft {
		h.slashForced = false
		h.slashDismissedQuery = ""
		if h.slashMenuOpen {
			h.setSlashMenuOpenLocked(false)
			h.slashItems = nil
			h.slashQuery = ""
			h.slashTitle = ""
			h.slashSelected = 0
			h.slashScroll = 0
			h.needsPaint = true
		}
		return
	}

	if h.slashDismissedQuery != "" && h.slashDismissedQuery == draft {
		return
	}
	h.slashDismissedQuery = ""
	h.slashForced = false

	if len(items) == 0 {
		if h.slashMenuOpen {
			h.setSlashMenuOpenLocked(false)
			h.slashItems = nil
			h.slashQuery = draft
			h.slashTitle = ""
			h.slashSelected = 0
			h.slashScroll = 0
			h.needsPaint = true
		}
		return
	}

	queryChanged := draft != h.slashQuery
	h.slashItems = items
	h.slashTitle = title
	h.slashQuery = draft
	if queryChanged {
		h.slashSelected = 0
		h.slashScroll = 0
	} else {
		h.slashSelected = min(max(0, h.slashSelected), len(items)-1)
	}
	h.setSlashMenuOpenLocked(true)
	h.needsPaint = true
}

// StepSlashMenu moves the highlighted command selection by delta and repaints immediately.
func (h *ChatHistory) StepSlashMenu(delta int) bool {
	if h == nil || h.closed.Load() || delta == 0 {
		return false
	}
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	if !h.slashMenuOpen || len(h.slashItems) == 0 {
		return false
	}
	n := len(h.slashItems)
	h.slashSelected = (h.slashSelected + delta%n + n) % n
	h.paintLocked()
	return true
}

// SlashMenuTabComplete returns the completion text for the currently highlighted slash command.
func (h *ChatHistory) SlashMenuTabComplete() (string, bool) {
	if h == nil || h.closed.Load() {
		return "", false
	}
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	if !h.slashMenuOpen || len(h.slashItems) == 0 {
		return "", false
	}
	idx := min(max(0, h.slashSelected), len(h.slashItems)-1)
	return h.slashItems[idx].TabText(), true
}

// CloseSlashMenu closes the slash menu and immediately restores the underlying chat viewport.
func (h *ChatHistory) CloseSlashMenu() {
	if h == nil || h.closed.Load() {
		return
	}
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	wasOpen := h.slashMenuOpen
	h.slashForced = false
	h.slashDismissedQuery = ""
	h.slashItems = nil
	h.slashQuery = ""
	h.slashTitle = ""
	h.slashSelected = 0
	h.slashScroll = 0
	h.setSlashMenuOpenLocked(false)
	if wasOpen && !h.captureOnly {
		h.paintLocked()
		h.redrawPromptSeparatorLocked()
	}
}

func (h *ChatHistory) shouldRenderSlashMenuLocked() bool {
	return !h.isNativeScroll() && h.slashMenuOpen && len(h.slashItems) > 0 && !h.browsing && !h.searchEditing && !h.captureOnly
}

func (h *ChatHistory) decorateSlashMenuLocked(rows []string) []string {
	if !h.shouldRenderSlashMenuLocked() || len(rows) < 2 || h.buffer == nil {
		h.slashMenuHeaderRow = 0
		h.slashMenuStartRow = 0
		h.slashMenuEndRow = 0
		h.slashVisibleStart = 0
		return rows
	}
	live := h.a.LiveSettings()
	theme := style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
	maxVisible := min(slashMenuMaxVisible, max(1, len(rows)-1))
	menuRows, visStart, visCount := render.RenderSlashMenuRows(
		h.slashItems, h.slashTitle, h.slashSelected, h.slashScroll, h.buffer.Width, maxVisible, theme,
	)
	if len(menuRows) == 0 || visCount == 0 {
		h.slashMenuHeaderRow = 0
		h.slashMenuStartRow = 0
		h.slashMenuEndRow = 0
		h.slashVisibleStart = 0
		return rows
	}
	h.slashScroll = visStart
	h.slashVisibleStart = visStart

	out := append([]string(nil), rows...)
	startIdx := len(out) - len(menuRows)
	for i, line := range menuRows {
		out[startIdx+i] = line
	}
	// Terminal rows are 1-based; row 0 of menuRows is the header line, rows 1..visCount are items.
	h.slashMenuHeaderRow = startIdx + 1
	h.slashMenuStartRow = startIdx + 2
	h.slashMenuEndRow = startIdx + len(menuRows)
	return out
}

func (h *ChatHistory) slashMenuHintLocked(width int, theme style.UITheme) string {
	if !h.shouldRenderSlashMenuLocked() {
		return ""
	}
	action := "Enter run"
	if idx := min(max(0, h.slashSelected), len(h.slashItems)-1); h.slashItems[idx].TakesArgs {
		action = "Enter fill"
	}
	text := "↑↓ move · Tab fill · " + action + " · Esc close"
	return style.NewStyle().Foreground(theme.TextMuted).Render(style.TruncateCells(text, max(1, width-1)))
}

func (h *ChatHistory) acceptSlashItemLocked(idx int) []byte {
	if len(h.slashItems) == 0 {
		return nil
	}
	idx = min(max(0, idx), len(h.slashItems)-1)
	item := h.slashItems[idx]
	replacement := item.AcceptText()
	h.reader.SetPendingLineReplace(replacement)
	h.slashForced = false
	if item.TakesArgs {
		h.slashDismissedQuery = ""
		return []byte(string(interceptor.PromptReplaceLineKey))
	}
	h.slashDismissedQuery = replacement
	h.setSlashMenuOpenLocked(false)
	h.slashItems = nil
	h.slashQuery = ""
	h.slashTitle = ""
	h.slashSelected = 0
	h.slashScroll = 0
	h.paintLocked()
	h.redrawPromptSeparatorLocked()
	return append([]byte(string(interceptor.PromptReplaceLineKey)), '\r')
}

func (h *ChatHistory) handleSlashMenuEnter() (handled bool, emit []byte) {
	if !h.slashMenuOpenAtomic.Load() || !h.reader.IsAtMainPrompt.Load() {
		return false, nil
	}
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	if !h.shouldRenderSlashMenuLocked() {
		return false, nil
	}
	idx := min(max(0, h.slashSelected), len(h.slashItems)-1)
	item := h.slashItems[idx]
	draft, _ := h.reader.GetPromptDraft()
	if !h.slashForced && !item.TakesArgs && strings.TrimSpace(draft) == item.Name {
		h.setSlashMenuOpenLocked(false)
		h.slashItems = nil
		h.slashQuery = ""
		h.slashTitle = ""
		h.slashSelected = 0
		h.slashScroll = 0
		h.paintLocked()
		h.redrawPromptSeparatorLocked()
		return false, nil
	}
	return true, h.acceptSlashItemLocked(idx)
}

func (h *ChatHistory) handleSlashMenuEscape() (bool, []byte) {
	if !h.slashMenuOpenAtomic.Load() || !h.reader.IsAtMainPrompt.Load() {
		return false, nil
	}
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	if !h.shouldRenderSlashMenuLocked() {
		return false, nil
	}
	draft, _ := h.reader.GetPromptDraft()
	h.slashForced = false
	h.setSlashMenuOpenLocked(false)
	if draft == "/" {
		h.slashDismissedQuery = ""
		h.slashItems = nil
		h.slashQuery = ""
		h.slashTitle = ""
		h.slashSelected = 0
		h.slashScroll = 0
		h.reader.SetPendingLineReplace("")
		h.paintLocked()
		h.redrawPromptSeparatorLocked()
		return true, []byte(string(interceptor.PromptReplaceLineKey))
	}
	if strings.HasPrefix(draft, "/") {
		h.slashDismissedQuery = draft
	}
	h.paintLocked()
	h.redrawPromptSeparatorLocked()
	return true, nil
}

func (h *ChatHistory) handleSlashMenuHover(x, y int) bool {
	if !h.slashMenuOpenAtomic.Load() {
		return false
	}
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	if !h.shouldRenderSlashMenuLocked() || h.slashMenuStartRow <= 0 {
		return false
	}
	if y < h.slashMenuHeaderRow || y > h.slashMenuEndRow {
		return false
	}
	if y >= h.slashMenuStartRow && y <= h.slashMenuEndRow {
		idx := h.slashVisibleStart + (y - h.slashMenuStartRow)
		if idx >= 0 && idx < len(h.slashItems) && idx != h.slashSelected {
			h.slashSelected = idx
			h.paintLocked()
		}
	}
	return true
}

func (h *ChatHistory) handleSlashMenuClick(x, y int) (handled bool, emit []byte) {
	if !h.slashMenuOpenAtomic.Load() {
		return false, nil
	}
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	if !h.shouldRenderSlashMenuLocked() || h.slashMenuStartRow <= 0 {
		return false, nil
	}
	if y >= h.slashMenuStartRow && y <= h.slashMenuEndRow {
		idx := h.slashVisibleStart + (y - h.slashMenuStartRow)
		if idx >= 0 && idx < len(h.slashItems) {
			return true, h.acceptSlashItemLocked(idx)
		}
		return true, nil
	}
	if y == h.slashMenuHeaderRow {
		return true, nil
	}
	// Clicking outside the dropdown dismisses it cleanly.
	draft, _ := h.reader.GetPromptDraft()
	h.slashForced = false
	h.setSlashMenuOpenLocked(false)
	if draft == "/" {
		h.slashDismissedQuery = ""
		h.slashItems = nil
		h.slashQuery = ""
		h.slashTitle = ""
		h.slashSelected = 0
		h.slashScroll = 0
		h.reader.SetPendingLineReplace("")
		h.paintLocked()
		h.redrawPromptSeparatorLocked()
		return true, []byte(string(interceptor.PromptReplaceLineKey))
	}
	if strings.HasPrefix(draft, "/") {
		h.slashDismissedQuery = draft
	}
	h.paintLocked()
	h.redrawPromptSeparatorLocked()
	return true, nil
}

func (h *ChatHistory) handleSlashMenuWheel(delta int, x, y int) bool {
	if !h.slashMenuOpenAtomic.Load() || delta == 0 {
		return false
	}
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	if !h.shouldRenderSlashMenuLocked() || h.slashMenuHeaderRow <= 0 {
		return false
	}
	if y < h.slashMenuHeaderRow || y > h.slashMenuEndRow {
		return false
	}
	visCount := max(1, h.slashMenuEndRow-h.slashMenuStartRow+1)
	maxStart := max(0, len(h.slashItems)-visCount)
	nextScroll := min(maxStart, max(0, h.slashScroll+delta))
	nextSel := min(len(h.slashItems)-1, max(0, h.slashSelected+delta))
	if nextSel < nextScroll {
		nextSel = nextScroll
	}
	if nextSel >= nextScroll+visCount {
		nextSel = nextScroll + visCount - 1
	}
	if nextScroll != h.slashScroll || nextSel != h.slashSelected {
		h.slashScroll = nextScroll
		h.slashSelected = nextSel
		h.paintLocked()
	}
	return true
}

func (h *ChatHistory) toggleCommandsButton() []byte {
	if h == nil || h.closed.Load() || h.reader == nil {
		return nil
	}
	if !h.reader.IsAtMainPrompt.Load() || h.reader.ActiveTurn.Load() {
		return nil
	}
	if h.slashMenuOpenAtomic.Load() {
		draft, _ := h.reader.GetPromptDraft()
		ui.TerminalMu.Lock()
		h.slashForced = false
		if strings.HasPrefix(draft, "/") && draft != "/" {
			h.slashDismissedQuery = draft
		} else {
			h.slashDismissedQuery = ""
		}
		h.setSlashMenuOpenLocked(false)
		h.slashItems = nil
		h.slashQuery = ""
		h.slashTitle = ""
		h.slashSelected = 0
		h.slashScroll = 0
		h.paintLocked()
		h.redrawPromptSeparatorLocked()
		ui.TerminalMu.Unlock()
		if draft == "/" {
			h.reader.SetPendingLineReplace("")
			return []byte(string(interceptor.PromptReplaceLineKey))
		}
		return nil
	}

	draft, _ := h.reader.GetPromptDraft()
	if draft == "" {
		// Inject '/' into the empty prompt so the user immediately gets the live filterable menu.
		return []byte("/")
	}
	if strings.HasPrefix(draft, "/") && !strings.ContainsAny(draft, "\r\n") {
		extras := h.loadExtraTopSlashItems()
		items, title := render.FilterSlashCommands(draft, extras, h.dynamicSubcommands)
		if len(items) == 0 {
			items, title = render.FilterSlashCommands("/", extras, h.dynamicSubcommands)
		}
		ui.TerminalMu.Lock()
		h.slashDismissedQuery = ""
		h.slashForced = false
		h.slashItems = items
		h.slashTitle = title
		h.slashQuery = draft
		h.slashSelected = 0
		h.slashScroll = 0
		if len(items) > 0 {
			h.setSlashMenuOpenLocked(true)
			h.paintLocked()
			h.redrawPromptSeparatorLocked()
		}
		ui.TerminalMu.Unlock()
		return nil
	}

	// Non-empty non-slash draft: open the full command picker without erasing the draft.
	extras := h.loadExtraTopSlashItems()
	items, title := render.FilterSlashCommands("/", extras, nil)
	ui.TerminalMu.Lock()
	h.slashDismissedQuery = ""
	h.slashForced = true
	h.slashItems = items
	h.slashTitle = title
	h.slashQuery = "/"
	h.slashSelected = 0
	h.slashScroll = 0
	if len(items) > 0 {
		h.setSlashMenuOpenLocked(true)
		h.paintLocked()
		h.redrawPromptSeparatorLocked()
	}
	ui.TerminalMu.Unlock()
	return nil
}
