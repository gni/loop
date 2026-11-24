package render

import (
	"loop/pkg/terminal"
	"loop/pkg/ui/render/chrome"
	"loop/pkg/ui/render/markdown"
	"loop/pkg/ui/render/scrollback"
	"loop/pkg/ui/render/syntax"
)

// Sync frame helpers.
var WrapSyncFrame = terminal.WrapSyncFrame

// Chrome types and helpers.
type (
	ConsoleControls  = chrome.ConsoleControls
	HoverTarget      = chrome.HoverTarget
	Hitbox           = chrome.Hitbox
	SlashCommandItem = chrome.SlashCommandItem
	StatusBarState   = chrome.StatusBarState
)

const (
	HoverNone           = chrome.HoverNone
	HoverCommandsButton = chrome.HoverCommandsButton
	HoverThemeButton    = chrome.HoverThemeButton
	HoverAutoBadge      = chrome.HoverAutoBadge
	HoverReasoningBadge = chrome.HoverReasoningBadge
	HoverGitBranch      = chrome.HoverGitBranch
	HoverCollapseBadge  = chrome.HoverCollapseBadge
)

var (
	HitTest                            = chrome.HitTest
	DrawConsoleControls                = chrome.DrawConsoleControls
	DrawStaticStatsLine                = chrome.DrawStaticStatsLine
	DrawStaticPromptSeparatorWithHover = chrome.DrawStaticPromptSeparatorWithHover
	DrawStatusBarWithHover             = chrome.DrawStatusBarWithHover
	FormatPromptSeparatorWithAuto      = chrome.FormatPromptSeparatorWithAuto
	FormatStaticStatsContent           = chrome.FormatStaticStatsContent
	FormatStatusBarLine                = chrome.FormatStatusBarLine
	LayoutPrompt                       = chrome.LayoutPrompt
	MovePromptVertical                 = chrome.MovePromptVertical
	FilterSlashCommands                = chrome.FilterSlashCommands
	RenderSlashMenuRows                = chrome.RenderSlashMenuRows
)

// Scrollback types and helpers.
type (
	Scrollback     = scrollback.Scrollback
	QuestionAnchor = scrollback.QuestionAnchor
)

const HistoryPageSize = scrollback.HistoryPageSize

var (
	NewScrollback           = scrollback.NewScrollback
	NewPagedScrollback      = scrollback.NewPagedScrollback
	ChatHistoryRows         = scrollback.ChatHistoryRows
	DrawChatViewport        = scrollback.DrawChatViewport
	DrawChatViewportOverlay = scrollback.DrawChatViewportOverlay
	DrawChatViewportAtomic  = scrollback.DrawChatViewportAtomic
)

// Markdown helpers.
var (
	RenderMarkdownContent          = markdown.RenderMarkdownContent
	RenderRawMarkdownContent       = markdown.RenderRawMarkdownContent
	RenderRawReasoningContent      = markdown.RenderRawReasoningContent
	RenderReasoningMarkdownContent = markdown.RenderReasoningMarkdownContent
)

// Syntax helpers.
var (
	ResolveSyntaxLanguage = syntax.ResolveSyntaxLanguage
	ShellOutputLanguage   = syntax.ShellOutputLanguage
	NewShellOutputWriter  = syntax.NewShellOutputWriter
)
