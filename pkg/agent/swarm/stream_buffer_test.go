package swarm

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"loop/pkg/agent"
	"loop/pkg/config"
)

// Defect #3: subagents must not share BaseAgent.CurrentStreamBuffer. Two agents running
// concurrently must tee into their own buffers, and the main loop's buffer (which
// pkg/ui/redraw.go reads) must stay untouched by subagent output.
func TestConcurrentSubagentsDoNotShareStreamBuffer(t *testing.T) {
	base := &agent.Agent{Config: &config.Config{}}

	alice := &MultiAgent{Name: "alice", BaseAgent: base}
	bob := &MultiAgent{Name: "bob", BaseAgent: base}

	// Each agent owns its own buffer object; nothing aliases the base agent's.
	alice.StreamBuffer = new(bytes.Buffer)
	bob.StreamBuffer = new(bytes.Buffer)
	base.CurrentStreamBuffer = new(bytes.Buffer)

	if alice.StreamBuffer == bob.StreamBuffer {
		t.Fatalf("subagents share a stream buffer")
	}
	if alice.StreamBuffer == base.CurrentStreamBuffer || bob.StreamBuffer == base.CurrentStreamBuffer {
		t.Fatalf("subagent buffer aliases BaseAgent.CurrentStreamBuffer")
	}

	var wg sync.WaitGroup
	for _, ma := range []*MultiAgent{alice, bob} {
		wg.Add(1)
		go func(ma *MultiAgent) {
			defer wg.Done()
			w := ma.streamBufferView()
			for i := 0; i < 200; i++ {
				_, _ = w.Write([]byte(ma.Name))
			}
		}(ma)
	}
	wg.Wait()

	if got := string(alice.streamBufferBytes()); got != strings.Repeat("alice", 200) {
		t.Fatalf("alice buffer contaminated: %q", got)
	}
	if got := string(bob.streamBufferBytes()); got != strings.Repeat("bob", 200) {
		t.Fatalf("bob buffer contaminated: %q", got)
	}
	if base.CurrentStreamBuffer.Len() != 0 {
		t.Fatalf("main loop buffer received subagent output: %q", base.CurrentStreamBuffer.String())
	}
}
