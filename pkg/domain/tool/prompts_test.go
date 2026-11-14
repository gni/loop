package tool

import (
	"strings"
	"testing"
)

func TestOperatingLoopCatalogMerge(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		custom string
		want   string
	}{
		{"legacy catalog retains workflow", `{"system_prompts":{"default_identity":"Custom role"}}`, "1. DISCOVER:"},
		{"explicit workflow replaces default", `{"system_prompts":{"operating_loop":"Inspect, edit, verify using project checks."}}`, "Inspect, edit, verify using project checks."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base, err := decodeCatalog(embeddedPromptCatalog)
			if err != nil {
				t.Fatal(err)
			}
			custom, err := decodeCatalog([]byte(tt.custom))
			if err != nil {
				t.Fatal(err)
			}
			mergeCatalog(&base, custom)
			if !strings.Contains(base.SystemPrompts.OperatingLoop, tt.want) {
				t.Fatalf("merged workflow %q does not contain %q", base.SystemPrompts.OperatingLoop, tt.want)
			}
		})
	}
}
