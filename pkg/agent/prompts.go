package agent

import (
	"loop/pkg/agent/tool"
	domaintool "loop/pkg/domain/tool"
)

// Re-export prompt domain types and single-source-of-truth catalogs so agents, UI,
// tests, and CLI commands can inspect or modify them from either package. The tool
// package (pkg/agent/tool/registry.go) is the canonical alias hub, so the shared
// names delegate to it rather than being re-declared here; only the prompt-catalog
// names that the hub does not expose are declared directly.
type ToolPrompt = domaintool.ToolPrompt
type SystemPrompts = domaintool.SystemPrompts

var MasterSystemPrompts = &domaintool.MasterSystemPrompts
var MasterToolPrompts = tool.MasterToolPrompts
var GetToolPrompt = tool.GetToolPrompt
var SetToolPrompt = tool.SetToolPrompt
var FormatParamDescription = tool.FormatParamDescription
var FormatToolDescription = tool.FormatToolDescription
var FormatToolSnippet = tool.FormatToolSnippet
var FormatToolGuidelines = tool.FormatToolGuidelines
var FormatCoreRules = domaintool.FormatCoreRules
var LoadPromptCatalog = domaintool.LoadPromptCatalog
var DefaultPromptCatalogPath = domaintool.DefaultPromptCatalogPath
var RuntimeMessage = tool.RuntimeMessage
var RuntimeMessagef = tool.RuntimeMessagef
var MasterRuntimeMessages = domaintool.MasterRuntimeMessages
