package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"loop/pkg/agent/run"
	"loop/pkg/agent/tool"
	"loop/pkg/buildinfo"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	transport "loop/pkg/transport/http"
	"loop/pkg/transport/sse"
)

type nativeUsage struct {
	Input      int `json:"input_tokens"`
	Output     int `json:"output_tokens"`
	CacheRead  int `json:"cache_read_input_tokens"`
	CacheWrite int `json:"cache_creation_input_tokens"`
	Details    struct {
		Cached int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

type nativeItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Text      string          `json:"text"`
	Input     json.RawMessage `json:"input"`
}

type nativeRequestInput struct {
	base            string
	modelName       string
	system          []string
	history         []db.Message
	tools           []tool.Tool
	maxTokens       int
	hasReasoningCap bool
}

type nativeProtocol interface {
	buildRequest(p *OpenAICompatibleProvider, in nativeRequestInput) (endpoint string, payload map[string]any, err error)
	handleEvent(s *nativeStreamState, data []byte) (done bool, err error)
}

func nativeProtocolFor(api string) (nativeProtocol, error) {
	switch api {
	case "responses":
		return responsesProtocol{}, nil
	case "messages":
		return anthropicProtocol{}, nil
	case "generate-content":
		return geminiProtocol{}, nil
	default:
		return nil, fmt.Errorf("unsupported provider API %q", api)
	}
}

func (p *OpenAICompatibleProvider) api() string {
	if p == nil || p.Config == nil {
		return "chat-completions"
	}
	if profile, exists := p.Config.Providers[p.Config.ActiveProvider]; exists && profile.API != "" {
		return profile.API
	}
	return "chat-completions"
}

func (p *OpenAICompatibleProvider) nativeRequest(messages []db.Message, tools []tool.Tool) (string, map[string]any, error) {
	return p.nativeRequestContext(nil, messages, tools)
}

func (p *OpenAICompatibleProvider) nativeRequestContext(ctx context.Context, messages []db.Message, tools []tool.Tool) (string, map[string]any, error) {
	if p == nil || p.Config == nil {
		return "", nil, fmt.Errorf("provider config is nil")
	}
	proto, err := nativeProtocolFor(p.api())
	if err != nil {
		return "", nil, err
	}
	base := strings.TrimRight(p.Config.Endpoint, "/")
	rawMessages := messages
	messages, _ = sanitizeRequestMessages(rawMessages)
	toolProto := toolProtocolForConfig(p.Config)
	tools = toolProto.PrepareWireTools(tools, p.Config.CompactPrompt)
	messages = toolProto.PrepareWireMessages(messages)
	var compactFallback func() []db.Message
	if historyCompressionEnabled(p.Config) {
		compactFallback = func() []db.Message {
			compacted := CompactHistoricalToolOutputs(rawMessages)
			valid, _ := sanitizeRequestMessages(compacted)
			return toolProto.PrepareWireMessages(valid)
		}
	}
	clean, maxTokens, err := p.budgetRequestContextWithFallback(ctx, messages, tools, compactFallback)
	if err != nil {
		return "", nil, err
	}
	clean = mergeAdjacentUserMessages(clean)

	var system []string
	var history []db.Message
	for i, msg := range nativeHistory(EnforceToolPairingInvariance(clean)) {
		if msg.Role == "system" {
			if i == 0 {
				system = append(system, msg.Content)
				continue
			}
			// Retain non-leading system messages (e.g. compaction continuity summaries)
			// in chronological sequence as conversation context turns, rather than hoisting
			// into the root system prompt (which corrupts prompt prefix caching).
			msg = db.Message{Role: "user", Content: msg.Content}
		}
		if msg.Role != "user" && msg.Role != "assistant" && msg.Role != "tool" {
			continue
		}
		history = append(history, msg)
	}

	caps := p.chatCapabilities()
	hasReasoningCap := len(p.selectedModel().Reasoning) > 0 || caps.ReasoningEffort || caps.ThinkingControls || p.ThinkingEnabled()
	return proto.buildRequest(p, nativeRequestInput{
		base:            base,
		modelName:       p.Config.ResolveModelAlias(p.Config.Model),
		system:          system,
		history:         history,
		tools:           tools,
		maxTokens:       maxTokens,
		hasReasoningCap: hasReasoningCap,
	})
}

type nativeStreamState struct {
	streamCtx          context.Context
	api                string
	chunks             chan<- StreamChunk
	observer           ChunkDropObserver
	result             *db.Message
	text               strings.Builder
	rawText            strings.Builder
	reasoning          strings.Builder
	textFilter         *FallbackToolTextFilter
	reasoningFilter    *FallbackToolTextFilter
	captureRawText     bool
	calls              map[int]*db.ToolCall
	argumentBuffers    toolArgumentBuffers
	itemIndexes        map[string]int
	thinking           map[int]*message.ContentBlock
	thinkingText       map[int]*strings.Builder
	thinkingSignatures map[int]*strings.Builder
	budget             responseBudget
	outputErr          error
	generationStart    time.Time
	inputTokens        int
	complete           bool
	finish             string
}

func (s *nativeStreamState) applyUsage(usage nativeUsage) {
	if usage.Input > 0 {
		s.inputTokens = max(s.inputTokens, usage.Input)
	}
	s.result.CacheReadTokens = max(s.result.CacheReadTokens, max(usage.CacheRead, usage.Details.Cached))
	s.result.CacheWriteTokens = max(s.result.CacheWriteTokens, usage.CacheWrite)
	if s.inputTokens > 0 || s.result.CacheReadTokens > 0 || s.result.CacheWriteTokens > 0 {
		if s.api == "messages" {
			s.result.PromptTokens = s.inputTokens + s.result.CacheReadTokens + s.result.CacheWriteTokens
		} else {
			s.result.PromptTokens = max(s.result.PromptTokens, s.inputTokens)
		}
	}
	if usage.Output > 0 {
		s.result.CompletionTokens = max(s.result.CompletionTokens, usage.Output)
	}
}

func (s *nativeStreamState) emit(kind, delta string) {
	if s.outputErr != nil {
		return
	}
	if s.outputErr = s.budget.add(delta); s.outputErr != nil {
		return
	}
	if delta != "" && s.generationStart.IsZero() {
		s.generationStart = time.Now()
	}
	if kind == "reasoning" {
		if s.reasoningFilter != nil {
			s.reasoningFilter.Write(delta)
			return
		}
		s.reasoning.WriteString(delta)
	} else {
		if s.captureRawText {
			s.rawText.WriteString(delta)
		}
		if s.textFilter != nil {
			s.textFilter.Write(delta)
			return
		}
		s.text.WriteString(delta)
	}
	emitChunkObserved(s.streamCtx, s.chunks, StreamChunk{Type: kind, Content: delta}, s.observer)
}

func (s *nativeStreamState) setCall(index int, item nativeItem) {
	if s.outputErr != nil {
		return
	}
	if index < 0 || len(s.calls) >= maxResponseToolCalls {
		s.outputErr = fmt.Errorf("invalid or excessive provider tool call index")
		return
	}
	_, knownItem := s.itemIndexes[item.ID]
	if s.calls[index] != nil || (item.ID != "" && knownItem) {
		s.outputErr = fmt.Errorf("duplicate provider tool call")
		return
	}
	if s.generationStart.IsZero() {
		s.generationStart = time.Now()
	}
	id := item.CallID
	if id == "" {
		id = item.ID
	}
	if id == "" {
		id = "call_" + db.NewUUID()
	}
	if item.Arguments == "" && len(item.Input) > 0 && string(item.Input) != "{}" {
		item.Arguments = string(item.Input)
	}
	if s.outputErr = s.budget.add(item.Name, item.Arguments); s.outputErr != nil {
		return
	}
	normName := tool.NormalizeName(item.Name)
	s.calls[index] = &db.ToolCall{ID: id, Type: "function", Function: db.ToolFunction{Name: normName, Arguments: item.Arguments}}
	s.argumentBuffers.replace(index, item.Arguments)
	if item.ID != "" {
		s.itemIndexes[item.ID] = index
	}
	emitChunkObserved(s.streamCtx, s.chunks, StreamChunk{Type: "tool_name", Content: normName, ToolCallIndex: index}, s.observer)
	if item.Arguments != "" {
		emitChunkObserved(s.streamCtx, s.chunks, StreamChunk{Type: "tool_call", Content: item.Arguments, ToolCallIndex: index}, s.observer)
	}
}

func (s *nativeStreamState) emitToolCallDelta(index int, delta string) {
	s.argumentBuffers.append(index, delta)
	emitChunkObserved(s.streamCtx, s.chunks, StreamChunk{Type: "tool_call", Content: delta, ToolCallIndex: index}, s.observer)
}

func (p *OpenAICompatibleProvider) streamNative(ctx context.Context, messages []db.Message, tools []tool.Tool, chunks chan<- StreamChunk) (*db.Message, error) {
	if p == nil || p.Config == nil {
		return nil, fmt.Errorf("provider config is nil")
	}
	api := p.api()
	proto, err := nativeProtocolFor(api)
	if err != nil {
		return nil, err
	}
	streamCtx, kick, timedOut, stop := startStreamWatchdog(ctx, p.streamTimeout())
	defer stop()
	if !p.ThinkingProbeChecked() {
		p.ProbeServerCapabilities(streamCtx)
		kick()
	}
	endpoint, payload, err := p.nativeRequestContext(streamCtx, messages, tools)
	if err != nil {
		return nil, err
	}
	estimatedInput := 0
	if budget := p.ContextBudgetSnapshot(); budget != nil {
		estimatedInput = budget.EstimatedInput
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	client := transport.NewResilientClient(transport.RetryConfig{
		MaxRetries: 3,
		BaseDelay:  time.Second,
		Client:     p.HTTPClient,
		BeforeAttempt: func(int) error {
			if err := p.checkContextOrTimeout(ctx, streamCtx, timedOut, "LLM request timed out after "); err != nil {
				return err
			}
			if err := run.RecordRequestAttempt(ctx); err != nil {
				return err
			}
			if counter, _ := ctx.Value(contextKeyHTTPAttempts).(*atomic.Int64); counter != nil {
				counter.Add(1)
			}
			return nil
		},
		DescribeError: func(err error) error {
			return fmt.Errorf("HTTP request failed: %w. Check your endpoint (%s)", err, transport.RedactURL(p.Config.Endpoint))
		},
	})
	response, err := client.DoWithRetry(streamCtx, func() (*http.Request, error) {
		request, err := http.NewRequestWithContext(streamCtx, "POST", endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP request: %w", err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "text/event-stream")
		request.Header.Set("loop", buildinfo.Version)
		if profile, ok := p.Config.Providers[p.Config.ActiveProvider]; ok {
			transport.ApplyProviderHeaders(request.Header, profile.Headers)
		}
		transport.ApplyProviderAuthHeaders(request.Header, api, p.requestKey(streamCtx))
		return request, nil
	})
	if err != nil {
		if timeout := p.checkContextOrTimeout(ctx, streamCtx, timedOut, "LLM request timed out after "); timeout != nil {
			return nil, timeout
		}
		return nil, err
	}
	kick()
	defer response.Body.Close()

	result := &db.Message{Role: "assistant", Provider: p.Config.ActiveProvider, Model: p.Config.Model}
	defer func() { p.requestCounter().observe(result.PromptTokens, estimatedInput) }()

	toolProto := toolProtocolForConfig(p.Config)
	state := &nativeStreamState{
		streamCtx:          streamCtx,
		api:                api,
		chunks:             chunks,
		observer:           p.chunkDropObserver(ctx),
		result:             result,
		captureRawText:     !toolProto.UsesNativeSchemas(),
		calls:              make(map[int]*db.ToolCall),
		argumentBuffers:    make(toolArgumentBuffers),
		itemIndexes:        make(map[string]int),
		thinking:           make(map[int]*message.ContentBlock),
		thinkingText:       make(map[int]*strings.Builder),
		thinkingSignatures: make(map[int]*strings.Builder),
	}
	state.textFilter = p.newStreamTextFilter(streamCtx, chunks, "text", &state.text)
	state.reasoningFilter = p.newReasoningTextFilter(streamCtx, chunks, &state.reasoning)

	finalized := false
	defer func() {
		if !finalized {
			state.textFilter.Flush()
			state.reasoningFilter.Flush()
			result.Content = state.text.String()
			result.ReasoningContent = toolProto.CleanReasoning(state.reasoning.String())
		}
	}()

	reader := sse.NewLimitedReader(response.Body, 128<<20)
	reader.OnActivity = kick
	for {
		packet, err := reader.Read(streamCtx)
		if err == io.EOF {
			break
		}
		if err != nil {
			if timeout := p.checkContextOrTimeout(ctx, streamCtx, timedOut, "LLM stream timed out after "); timeout != nil {
				return result, timeout
			}
			return result, err
		}
		data := strings.TrimSpace(packet.Data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
		done, err := proto.handleEvent(state, []byte(data))
		if err != nil {
			return result, err
		}
		if state.outputErr != nil {
			return result, state.outputErr
		}
		if done {
			break
		}
	}

	state.textFilter.Flush()
	state.reasoningFilter.Flush()
	result.Content = state.text.String()
	result.ReasoningContent = toolProto.CleanReasoning(state.reasoning.String())
	result.StopReason = state.finish
	finalized = true

	if !state.complete || state.finish == "" {
		return result, fmt.Errorf("provider stream ended before completion: %w", ErrIncompleteResponse)
	}
	switch state.finish {
	case "length", "max_tokens", "MAX_TOKENS":
		return result, fmt.Errorf("provider stopped with %s: %w", state.finish, ErrOutputTruncated)
	case "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL":
		return result, fmt.Errorf("provider stopped with %s: %w", state.finish, ErrIncompleteResponse)
	case "SAFETY", "RECITATION", "content_filter", "PROHIBITED_CONTENT", "BLOCKLIST", "SPII", "IMAGE_SAFETY", "refusal":
		return result, fmt.Errorf("provider stopped with incomplete outcome %s", state.finish)
	}

	state.argumentBuffers.flush(state.calls)
	if err := validateProviderCalls(state.calls); err != nil {
		return result, err
	}
	var nativeCalls []db.ToolCall
	if len(state.calls) > 0 {
		nativeCalls = orderedToolCalls(state.calls)
	}
	rawSource := result.Content
	if state.captureRawText {
		rawSource = state.rawText.String()
	}
	parsedCalls, cleanContent, err := toolProto.ParseAssistantToolCalls(rawSource, nativeCalls)
	if err != nil {
		return result, err
	}
	result.ToolCalls = parsedCalls
	if len(parsedCalls) > 0 && state.captureRawText {
		result.Content = cleanContent
	}

	indexes := make([]int, 0, len(state.thinking))
	for index := range state.thinking {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		state.thinking[index].Text = state.thinkingText[index].String()
		state.thinking[index].Signature = state.thinkingSignatures[index].String()
		result.ContentBlocks = append(result.ContentBlocks, *state.thinking[index])
	}
	if !hasUsableAssistantResponse(result) {
		return result, ErrEmptyResponse
	}
	var duration time.Duration
	if !state.generationStart.IsZero() {
		duration = time.Since(state.generationStart)
	}
	if callback, ok := ctx.Value(contextKeyGenerationDuration).(func(time.Duration)); ok {
		callback(duration)
	}
	return result, nil
}
