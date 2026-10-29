package tool

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPluginDiscoveryDoesNotExecuteBinary(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plugins")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "executed")
	plugin := filepath.Join(dir, "probe")
	script := "#!/bin/sh\nprintf 'executed' > '" + marker + "'\n"
	if err := os.WriteFile(plugin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	registry := NewToolRegistry()
	if err := RegisterPluginsContext(context.Background(), registry, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("plugin executed during discovery: %v", err)
	}
	if _, present := registry.Lookup("plugin__probe"); present {
		t.Fatal("binary without descriptor should not be registered")
	}
	definition := `{"name":"probe","description":"test","parameters":{"type":"object","properties":{}}}`
	if err := os.WriteFile(plugin+".json", []byte(definition), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RegisterPluginsContext(context.Background(), registry, dir); err != nil {
		t.Fatal(err)
	}
	if _, present := registry.Lookup("plugin__probe"); !present {
		t.Fatal("valid declarative plugin not discovered")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("descriptor lookup executed plugin: %v", err)
	}
}
