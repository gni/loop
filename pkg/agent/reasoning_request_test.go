package agent

import (
    "encoding/json"
    "testing"

    "loop/pkg/config"
    "loop/pkg/db"
)

func reasoningWire(t *testing.T, effort string, caps config.ChatCapabilities) map[string]json.RawMessage {
    t.Helper()
    cfg := testRequestConfig("http://localhost:1234/v1")
    cfg.ReasoningEffort = effort
    cfg.ChatCapabilities = caps
    provider := &OpenAICompatibleProvider{Config: cfg}
    _, _, raw, err := provider.prepareChatCompletionRequest([]db.Message{{Role: "user", Content: "hello"}}, nil)
    if err != nil {
        t.Fatal(err)
    }
    var payload map[string]json.RawMessage
    if err := json.Unmarshal(raw, &payload); err != nil {
        t.Fatal(err)
    }
    return payload
}

func TestReasoningEffortRequestMapping(t *testing.T) {
    cases := []struct{ selected, sent string }{
        {"off", ""},
        {"low", "low"},
        {"medium", "medium"},
        {"high", "high"},
        {"max", "high"},
    }
    for _, tc := range cases {
        t.Run(tc.selected, func(t *testing.T) {
            fields := reasoningWire(t, tc.selected, config.ChatCapabilities{ReasoningEffort: true})
            value, exists := fields["reasoning_effort"]
            if tc.sent == "" {
                if exists {
                    t.Fatalf("off must not send reasoning_effort: %s", value)
                }
                return
            }
            if !exists {
                t.Fatalf("reasoning_effort %s not sent", tc.sent)
            }
            var sent string
            if err := json.Unmarshal(value, &sent); err != nil || sent != tc.sent {
                t.Fatalf("reasoning_effort = %s, expected %s: %v", value, tc.sent, err)
            }
        })
    }
}

func TestReasoningEffortRequiresCapability(t *testing.T) {
    fields := reasoningWire(t, "low", config.ChatCapabilities{})
    if value, ok := fields["reasoning_effort"]; ok {
        t.Fatalf("unadvertised reasoning_effort was sent: %s", value)
    }
}

func TestThinkingControlsDoNotDependOnPropsProbe(t *testing.T) {
    // The selected provider opts into this non-standard API. An unavailable
    // /props endpoint must not override the explicitly configured capability.
    for _, tc := range []struct {
        effort string
        enabled bool
        format string
    }{
        {"off", false, "none"},
        {"low", true, "auto"},
        {"high", true, "auto"},
    } {
        t.Run(tc.effort, func(t *testing.T) {
            fields := reasoningWire(t, tc.effort, config.ChatCapabilities{ThinkingControls: true})
            if string(fields["reasoning_control"]) != "true" {
                t.Fatalf("reasoning_control is missing when explicitly enabled")
            }
            var format string
            if err := json.Unmarshal(fields["reasoning_format"], &format); err != nil || format != tc.format {
                t.Fatalf("reasoning_format = %q, expected %q: %v", format, tc.format, err)
            }
            var kwargs ChatTemplateKwargs
            if err := json.Unmarshal(fields["chat_template_kwargs"], &kwargs); err != nil {
                t.Fatal(err)
            }
            if kwargs.EnableThinking != tc.enabled {
                t.Fatalf("enable_thinking = %v, expected %v", kwargs.EnableThinking, tc.enabled)
            }
        })
    }
}
