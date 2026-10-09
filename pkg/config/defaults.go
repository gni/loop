package config

import _ "embed"

//go:embed defaults/config.json
var embeddedDefaultConfig []byte

//go:embed defaults/providers.json
var embeddedDefaultProviders []byte

//go:embed defaults/mcp.json
var embeddedDefaultMCP []byte
