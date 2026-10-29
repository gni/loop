package agent

import (
	"loop/pkg/db"
	"testing"
)

func TestToolBatchSealPreservesEarlyCompletion(t *testing.T) {
	batch := NewToolBatch(nil)
	batch.Observe(0, "write", `{"path":"a"}`)
	batch.Start(0)
	batch.SetResult(0, "done", nil)
	batch.Finish(0, false)
	batch.Seal([]db.ToolCall{{ID: "provider-call", Function: db.ToolFunction{Name: "write", Arguments: `{"path":"a"}`}}})
	call, err := batch.Snapshot(0)
	if err != nil {
		t.Fatal(err)
	}
	if call.Phase != ToolPhaseCompleted || call.ProviderID != "provider-call" || call.Output != "done" {
		t.Fatalf("early execution was overwritten: %+v", call)
	}
}
