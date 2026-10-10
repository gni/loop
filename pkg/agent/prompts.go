package agent

import (
	domaintool "loop/pkg/domain/tool"
)

// Re-export prompt domain types and single-source-of-truth catalogs
// so agents, UI, tests, and CLI commands can inspect or modify them from either package.
type ToolPrompt = domaintool.ToolPrompt
type SystemPrompts = domaintool.SystemPrompts

var MasterSystemPrompts = &domaintool.MasterSystemPrompts
var MasterToolPrompts = domaintool.MasterToolPrompts
var GetToolPrompt = domaintool.GetToolPrompt
var SetToolPrompt = domaintool.SetToolPrompt
var FormatParamDescription = domaintool.FormatParamDescription
var FormatToolDescription = domaintool.FormatToolDescription
var FormatToolSnippet = domaintool.FormatToolSnippet
var FormatToolGuidelines = domaintool.FormatToolGuidelines
var FormatCoreRules = domaintool.FormatCoreRules
var LoadPromptCatalog = domaintool.LoadPromptCatalog
var DefaultPromptCatalogPath = domaintool.DefaultPromptCatalogPath
var RuntimeMessage = domaintool.RuntimeMessage
var RuntimeMessagef = domaintool.RuntimeMessagef
var MasterRuntimeMessages = domaintool.MasterRuntimeMessages
