package search

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	domaintool "loop/pkg/domain/tool"
)

// Every search tool description must state its numeric limits, and every notice
// must be actionable: the exact next offset plus the total count.
func TestSearchDescriptionsStateNumericLimits(t *testing.T) {
	cases := []struct {
		tool   domaintool.ToolExecutor
		expect string
	}{
		{NewGrepTool(), "Returns up to 100 matches (max 500)"},
		{NewFindTool(), "Returns up to 500 results (max 2000)"},
		{NewListTool(), "Lists up to 150 entries (max 1000)"},
	}
	for _, c := range cases {
		def := c.tool.Definition()
		if !strings.Contains(def.Function.Description, c.expect) {
			t.Errorf("%s description missing limit contract %q: %s", c.tool.Name(), c.expect, def.Function.Description)
		}
		if _, ok := def.Function.Parameters.Properties["offset"]; !ok {
			t.Errorf("%s must expose an offset parameter for pagination", c.tool.Name())
		}
	}
}

func TestSearchNoticesAreActionableAndPaginable(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &mockAgentContext{workspaceRoot: tmpDir}

	long := make([]string, 200)
	for i := range long {
		long[i] = "matchx " + strings.Repeat("é", 600)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "big.txt"), []byte(strings.Join(long, "\n")), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	g := NewGrepTool()
	out, err := g.Execute(ctx, `{"pattern":"matchx","path":"."}`)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	// Whichever budget is hit first, the notice must state the total and the
	// exact next offset.
	if !strings.Contains(out, "of 200 total") || !strings.Contains(out, "offset=") {
		t.Fatalf("grep notice is not actionable: %s", out)
	}

	// Using the offset the notice reported yields the next page without
	// repeating the previous page.
	out2, err := g.Execute(ctx, `{"pattern":"matchx","path":".","limit":10,"offset":11}`)
	if err != nil {
		t.Fatalf("grep offset: %v", err)
	}
	if !strings.Contains(out2, "offset=21") {
		t.Errorf("second page must not repeat the first page notice: %s", out2)
	}

	for i := 1; i <= 200; i++ {
		if err := os.WriteFile(filepath.Join(tmpDir, fmt.Sprintf("f%03d.txt", i)), []byte("x"), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	f := NewFindTool()
	fout, err := f.Execute(ctx, `{"pattern":"*.txt","limit":3}`)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !strings.Contains(fout, "of 201 total") || !strings.Contains(fout, "offset=4") {
		t.Errorf("find notice is not actionable: %s", fout)
	}

	l := NewListTool()
	lout, err := l.Execute(ctx, `{"path":"."}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(lout, "capped at 150 entries of 201 total") {
		t.Errorf("list notice is not actionable: %s", lout)
	}

	// A UTF-8 boundary cut never produces mojibake: the notice itself and the
	// retained lines must remain valid text.
	if err := os.WriteFile(filepath.Join(tmpDir, "utf8.txt"), []byte("matchx éééééééééé"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	uout, err := g.Execute(ctx, `{"pattern":"matchx","path":"utf8.txt"}`)
	if err != nil {
		t.Fatalf("grep utf8: %v", err)
	}
	if !strings.Contains(uout, "é") {
		t.Errorf("UTF-8 content damaged: %q", uout)
	}
}
