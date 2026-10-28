package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"loop/pkg/config"
	"loop/pkg/db"
)

func TestNativeProviderStreamingContracts(t *testing.T) {
	cases := []struct{ api, path, events string }{
		{"responses", "/v1/responses", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n\n"},
		{"messages", "/v1/messages", "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\ndata: {\"type\":\"message_stop\"}\n\n"},
		{"generate-content", "/v1beta/models/test:streamGenerateContent", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":2}}\n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.api, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("path %s, want %s", r.URL.Path, tc.path)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tc.events)
			}))
			defer server.Close()
			cfg := config.DefaultConfig()
			cfg.Model = "test"
			cfg.Endpoint = server.URL
			cfg.ActiveProvider = "test"
			cfg.Providers = map[string]config.ProviderConfig{"test": {API: tc.api, Endpoint: server.URL}}
			provider := &OpenAICompatibleProvider{Config: cfg, HttpClient: server.Client()}
			message, err := provider.StreamChatCompletions(context.Background(), []db.Message{{Role: "user", Content: "hi"}}, nil, make(chan StreamChunk, 10))
			if err != nil {
				t.Fatal(err)
			}
			if message.Content != "hello" || message.PromptTokens != 10 || message.CompletionTokens != 2 {
				t.Fatalf("incorrect response: %+v", message)
			}
		})
	}
}

func TestNativeToolCallsRequireConfirmedCompletion(t *testing.T){
 cases:=[]struct{api,events string}{
  {"responses","data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"item\",\"call_id\":\"call\",\"name\":\"read\",\"arguments\":\"\"}}\n\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"item\",\"delta\":\"{\\\"path\\\":\\\"a.txt\\\"}\"}\n\n"},
  {"messages","data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call\",\"name\":\"read\",\"input\":{}}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"a.txt\\\"}\"}}\n\n"},
  {"generate-content","data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"id\":\"call\",\"name\":\"read\",\"args\":{\"path\":\"a.txt\"}}}]}}]}\n\n"},
 }
 endings:=map[string]string{"responses":"data: {\"type\":\"response.completed\"}\n\n","messages":"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n","generate-content":"data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n"}
 for _,tc:=range cases{for _,completed:=range []bool{false,true}{t.Run(fmt.Sprintf("%s/%v",tc.api,completed),func(t *testing.T){
  server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){w.Header().Set("Content-Type","text/event-stream");fmt.Fprint(w,tc.events);if completed{fmt.Fprint(w,endings[tc.api])}}))
  defer server.Close()
  cfg:=config.DefaultConfig();cfg.Endpoint=server.URL;cfg.Model="test";cfg.ActiveProvider="test";cfg.Providers=map[string]config.ProviderConfig{"test":{Endpoint:server.URL,API:tc.api}}
  provider:=&OpenAICompatibleProvider{Config:cfg,HttpClient:server.Client()}
  response,err:=provider.StreamChatCompletions(context.Background(),[]db.Message{{Role:"user",Content:"read"}},nil,make(chan StreamChunk,20))
  if !completed{if err==nil{t.Fatal("interrupted tool call was confirmed")};return}
  if err!=nil{t.Fatal(err)}
  if len(response.ToolCalls)!=1||response.ToolCalls[0].Function.Name!="read"||response.ToolCalls[0].Function.Arguments!=`{"path":"a.txt"}`{t.Fatalf("tool call lost: %+v",response)}
 })}}
}
