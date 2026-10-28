package agent

import "testing"

func TestPermissionSubscriberIdentityAndFailure(t *testing.T) {
	bus := NewEventBus()
	first := bus.On(func(ToolCallEvent) *PermissionDecision { return nil })
	second := bus.On(func(ToolCallEvent) *PermissionDecision { return &PermissionDecision{Allowed: false} })
	first()
	bus.On(func(ToolCallEvent) *PermissionDecision { return &PermissionDecision{Allowed: false, Reason: "last"} })
	second()
	second()
	if decision := bus.Emit(ToolCallEvent{}); decision.Allowed || decision.Reason != "last" {
		t.Fatalf("wrong subscriber removed: %+v", decision)
	}
	bus = NewEventBus()
	bus.On(func(ToolCallEvent) *PermissionDecision { panic("failure") })
	if bus.Emit(ToolCallEvent{}).Allowed {
		t.Fatal("permission handler panic allowed execution")
	}
}
