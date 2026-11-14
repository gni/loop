package swarm

import (
	"io"
	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/ui/style"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveParentRestoresChildren(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.MaxSubagentDepth = 4
	base := agent.NewAgentAt(cfg, "", nil, t.TempDir())
	mam := NewMultiAgentManager(base, io.Discard, style.GetTheme("plain"))
	mam.staging = true
	mam.agentsDir = t.TempDir()
	defer mam.Close()
	if err := mam.SpawnAgent("parent", "Inspect files", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := mam.SpawnAgent("child", "Inspect files", "parent", nil); err != nil {
		t.Fatal(err)
	}
	if err := mam.RemoveAgent("parent"); err != nil {
		t.Fatal(err)
	}
	if got := mam.GetParentName("child"); got != "" {
		t.Fatalf("live parent %q", got)
	}
	store, err := mam.getStorage()
	if err != nil {
		t.Fatal(err)
	}
	def, err := store.LoadDefinition("child")
	if err != nil {
		t.Fatal(err)
	}
	if def.ParentName != "" {
		t.Errorf("persisted child still references removed parent %q", def.ParentName)
	}
	restored := NewMultiAgentManager(base, io.Discard, style.GetTheme("plain"))
	restored.staging = true
	restored.agentsDir = mam.agentsDir
	defer restored.Close()
	if err := restored.LoadSavedAgents(); err != nil {
		t.Errorf("restart failed: %v", err)
	}
}

func TestRemovalRollsBackChildrenOnFailure(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.MaxSubagentDepth = 4
	base := agent.NewAgentAt(cfg, "", nil, t.TempDir())
	mam := NewMultiAgentManager(base, io.Discard, style.GetTheme("plain"))
	mam.staging, mam.agentsDir = true, t.TempDir()
	defer mam.Close()
	for _, name := range []string{"parent", "child_a", "child_b"} {
		parent := "parent"
		if name == "parent" {
			parent = ""
		}
		if err := mam.SpawnAgent(name, "Inspect files", parent, nil); err != nil {
			t.Fatal(err)
		}
	}
	// The first child's write succeeds before loading the second child fails.
	if err := os.Rename(filepath.Join(mam.agentsDir, "child_b.json"), filepath.Join(mam.agentsDir, "child_b.backup")); err != nil {
		t.Fatal(err)
	}
	if err := mam.RemoveAgent("parent"); err == nil {
		t.Fatal("expected persistence failure")
	}
	if !mam.HasAgent("parent") || mam.GetParentName("child_a") != "parent" {
		t.Fatal("failed removal changed the live hierarchy")
	}
	store, err := mam.getStorage()
	if err != nil {
		t.Fatal(err)
	}
	def, err := store.LoadDefinition("child_a")
	if err != nil {
		t.Fatal(err)
	}
	if def.ParentName != "parent" {
		t.Fatalf("first child was not restored: parent=%q", def.ParentName)
	}
	if _, err := store.LoadDefinition("parent"); err != nil {
		t.Fatalf("failed removal lost parent: %v", err)
	}
}

func TestRemoveParentWhileReadingDepth(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.MaxSubagentDepth = 4
	base := agent.NewAgentAt(cfg, "", nil, t.TempDir())
	mam := NewMultiAgentManager(base, io.Discard, style.GetTheme("plain"))
	mam.staging, mam.ephemeral = true, true
	defer mam.Close()
	if err := mam.SpawnAgent("parent", "Inspect files", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := mam.SpawnAgent("child", "Inspect files", "parent", nil); err != nil {
		t.Fatal(err)
	}
	child := mam.Agents["child"]
	started, stop, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		close(started)
		for {
			select {
			case <-stop:
				return
			default:
				child.Depth()
			}
		}
	}()
	<-started
	err := mam.RemoveAgent("parent")
	close(stop)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if depth := child.Depth(); depth != 0 {
		t.Fatalf("reparented depth = %d, want 0", depth)
	}
}

func TestRemoveIntermediateParentPreservesGrandparent(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.MaxSubagentDepth = 4
	base := agent.NewAgentAt(cfg, "", nil, t.TempDir())
	mam := NewMultiAgentManager(base, io.Discard, style.GetTheme("plain"))
	mam.staging, mam.agentsDir = true, t.TempDir()
	defer mam.Close()
	for _, node := range []struct{ name, parent string }{{"root", ""}, {"middle", "root"}, {"leaf", "middle"}} {
		if err := mam.SpawnAgent(node.name, "Inspect files", node.parent, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := mam.RemoveAgent("middle"); err != nil {
		t.Fatal(err)
	}
	if got := mam.GetParentName("leaf"); got != "root" {
		t.Fatalf("leaf parent = %q, want root", got)
	}
	restored := NewMultiAgentManager(base, io.Discard, style.GetTheme("plain"))
	restored.staging, restored.agentsDir = true, mam.agentsDir
	defer restored.Close()
	if err := restored.LoadSavedAgents(); err != nil {
		t.Fatal(err)
	}
	if got := restored.GetParentName("leaf"); got != "root" {
		t.Fatalf("restored leaf parent = %q, want root", got)
	}
}
