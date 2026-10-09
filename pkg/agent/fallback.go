package agent

import (
	"loop/pkg/agent/fallback"
)

type FallbackToolTextFilter = fallback.ToolTextFilter

var ParseFallbackToolCalls = fallback.ParseFallbackToolCalls
var StripFallbackToolMarkup = fallback.StripFallbackToolMarkup
var NewFallbackToolTextFilter = fallback.NewToolTextFilter
