package config

import "testing"

func TestMCPServersEqualDetectsChanges(t *testing.T) {
	base := map[string]MCPServerConfig{
		"codegraph": {Type: "stdio", Command: "codegraph", Args: []string{"serve", "--mcp"}},
	}

	if !MCPServersEqual(base, CopyMCPServers(base)) {
		t.Error("a deep copy must compare equal to the original")
	}

	// Map values are not addressable, so a change has to be written back through a
	// local copy of the entry.
	changed := CopyMCPServers(base)
	entry := changed["codegraph"]
	entry.Args = append(entry.Args, "--project", "/tmp")
	changed["codegraph"] = entry
	if MCPServersEqual(base, changed) {
		t.Error("args change must be detected")
	}

	added := CopyMCPServers(base)
	added["other"] = MCPServerConfig{URL: "https://example.invalid/sse"}
	if MCPServersEqual(base, added) {
		t.Error("added server must be detected")
	}

	// Escape-exit with no edits is the case the reconnect guard exists for: it must
	// report no change so the caller skips paying for a reconnect.
	if !MCPServersEqual(base, CopyMCPServers(base)) {
		t.Error("unchanged config must report equal")
	}

	toggled := CopyMCPServers(base)
	entry = toggled["codegraph"]
	entry.Disabled = true
	toggled["codegraph"] = entry
	if MCPServersEqual(base, toggled) {
		t.Error("disabled flag must be detected")
	}

	envChanged := CopyMCPServers(base)
	entry = envChanged["codegraph"]
	entry.Env = map[string]string{"X": "1"}
	envChanged["codegraph"] = entry
	if MCPServersEqual(base, envChanged) {
		t.Error("env change must be detected")
	}
}

func TestCopyMCPServersDoesNotAliasInnerMaps(t *testing.T) {
	// The interactive editor mutates the map it is handed. A shallow struct copy would
	// make the snapshot useless for change detection, so the inner slices and maps must
	// be independent.
	original := map[string]MCPServerConfig{
		"s": {Args: []string{"a", "b"}, Env: map[string]string{"K": "V"}, Headers: map[string]string{"H": "1"}},
	}
	snapshot := CopyMCPServers(original)

	entry := original["s"]
	entry.Args[0] = "mutated"
	entry.Env["K"] = "mutated"
	entry.Headers["H"] = "mutated"
	original["s"] = entry

	if snapshot["s"].Args[0] != "a" {
		t.Errorf("args aliased: %v", snapshot["s"].Args)
	}
	if snapshot["s"].Env["K"] != "V" {
		t.Errorf("env aliased: %v", snapshot["s"].Env)
	}
	if snapshot["s"].Headers["H"] != "1" {
		t.Errorf("headers aliased: %v", snapshot["s"].Headers)
	}

	if CopyMCPServers(nil) != nil {
		t.Error("nil input must return nil")
	}
}
