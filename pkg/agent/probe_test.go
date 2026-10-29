package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"loop/pkg/config"
)

func TestPropsOnlyConfirmsExplicitReasoningSupport(t *testing.T) {
	for _, tc := range []struct {
		name  string
		props string
		want  bool
	}{
		{"context-only", `{"n_ctx":8192}`, false},
		{"explicit-support", `{"n_ctx":8192,"supports_reasoning":true}`, true},
		{"explicit-denial", `{"n_ctx":8192,"supports_reasoning":false}`, false},
		{"invalid-json", `broken`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/props" {
					http.NotFound(w, r)
					return
				}
				if got := r.URL.Query().Get("model"); got != "local model/v1" {
					t.Errorf("model query not escaped properly: %q", got)
				}
				_, _ = fmt.Fprint(w, tc.props)
			}))
			defer server.Close()
			cfg := config.DefaultConfig()
			cfg.Endpoint = server.URL + "/v1"
			cfg.Model = "local model/v1"
			p := &OpenAICompatibleProvider{Config: cfg, HttpClient: server.Client()}
			p.ProbeServerCapabilities(context.Background())
			if got := p.ThinkingEnabled(); got != tc.want {
				t.Errorf("thinking supported=%t, want %t", got, tc.want)
			}
			if tc.name != "invalid-json" && p.GetDetectedContextLimit() != 8192 {
				t.Errorf("context limit=%d", p.GetDetectedContextLimit())
			}
		})
	}
}
