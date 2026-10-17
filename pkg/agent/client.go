package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"loop/pkg/config"
	"loop/pkg/domain/limits"
)

type mcpClient struct {
	name           string
	config         config.MCPServerConfig
	isSSE          bool
	isStdio        bool
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	postURL        string
	sseBody        io.ReadCloser
	requests       map[int64]chan string
	requestMu      sync.Mutex
	closed         bool
	writeMu        sync.Mutex
	nextID         int64
	initialized    bool
	client         *http.Client
	cancel         context.CancelFunc
	connectTimeout time.Duration
}

type MCPTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

func (c *mcpClient) applyHeaders(req *http.Request, contentType string) {
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range c.config.Headers {
		req.Header.Set(k, v)
	}
}

func (c *mcpClient) postJSON(ctx context.Context, payload []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.postURL, bytes.NewBuffer(payload))
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req, "application/json")
	return c.client.Do(req)
}

func decodeRPCResponse(raw string, resultDest interface{}) error {
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return err
	}
	if env.Error != nil {
		return fmt.Errorf("MCP error: %s", env.Error.Message)
	}
	if len(env.Result) == 0 || string(env.Result) == "null" {
		return fmt.Errorf("MCP response missing result")
	}
	if resultDest != nil {
		return json.Unmarshal(env.Result, resultDest)
	}
	return nil
}

func (c *mcpClient) completeHandshake() error {
	if err := c.handshake(); err != nil {
		c.close()
		return fmt.Errorf("handshake failed: %w", err)
	}
	return nil
}

func (c *mcpClient) start() error {
	if strings.EqualFold(c.config.Type, "stdio") || (c.config.URL == "" && c.config.Command != "") {
		c.isStdio = true
		return c.startStdio()
	}
	if c.config.URL == "" {
		return fmt.Errorf("MCP server '%s' is missing 'url' or 'command' config", c.name)
	}
	c.isSSE = true
	return c.startSSE()
}

func (c *mcpClient) startSSE() error {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	req, err := http.NewRequestWithContext(ctx, "GET", c.config.URL, nil)
	if err != nil {
		cancel()
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	c.applyHeaders(req, "")

	sseClient := *c.client
	sseClient.Timeout = 0
	if transport, ok := c.client.Transport.(*http.Transport); ok && transport != nil {
		cloned := transport.Clone()
		cloned.ResponseHeaderTimeout = c.connectTimeout
		sseClient.Transport = cloned
	} else {
		sseClient.Transport = &http.Transport{ResponseHeaderTimeout: c.connectTimeout}
	}

	resp, err := sseClient.Do(req)
	if err != nil {
		cancel()
		return err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return fmt.Errorf("HTTP error %d", resp.StatusCode)
	}

	c.sseBody = resp.Body
	endpointChan := make(chan string, 1)

	go func() {
		defer c.failPendingRequests()
		defer close(endpointChan)
		scanner := bufio.NewScanner(c.sseBody)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		var lastEvent string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event:") {
				lastEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			} else if strings.HasPrefix(line, "data:") {
				data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if lastEvent == "endpoint" {
					postURL := c.resolveEndpointURL(data)
					if postURL != "" {
						mcpLog("[MCP] Resolved POST endpoint: %s\n", postURL)
					}
					select {
					case endpointChan <- postURL:
					default:
					}
				} else {
					mcpLog("[MCP Rx] %s\n", data)
					c.handleMessage(data)
				}
				lastEvent = ""
			}
		}
		if err := scanner.Err(); err != nil {
			mcpLog("SSE connection error for '%s': %v\n", c.name, err)
		} else {
			mcpLog("SSE connection closed for '%s'\n", c.name)
		}
	}()

	select {
	case postURL, ok := <-endpointChan:
		if !ok {
			c.close()
			return fmt.Errorf("MCP server '%s' closed its SSE stream before providing an endpoint", c.name)
		}
		if postURL == "" {
			c.close()
			return fmt.Errorf("MCP server '%s' supplied an invalid or cross-origin endpoint", c.name)
		}
		c.postURL = postURL
	case <-time.After(c.connectTimeout):
		c.close()
		return fmt.Errorf("timeout waiting for SSE endpoint initialization (%s)", c.connectTimeout)
	}

	return c.completeHandshake()
}

func (c *mcpClient) resolveEndpointURL(endpoint string) string {
	if strings.TrimSpace(endpoint) == "" {
		return ""
	}
	base, err := url.Parse(c.config.URL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return ""
	}
	ref, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || ref.User != nil {
		return ""
	}
	resolved := base.ResolveReference(ref)
	if resolved.Scheme != base.Scheme || !strings.EqualFold(resolved.Host, base.Host) || resolved.User != nil {
		return ""
	}
	return resolved.String()
}

func (c *mcpClient) failPendingRequests() {
	c.requestMu.Lock()
	c.closed = true
	for id, ch := range c.requests {
		close(ch)
		delete(c.requests, id)
	}
	c.requestMu.Unlock()
}

func (c *mcpClient) close() {
	c.writeMu.Lock()
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	c.writeMu.Unlock()
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		cmd := c.cmd
		go func() {
			_ = cmd.Wait()
		}()
	}
	if c.cancel != nil {
		c.cancel()
	}
	if c.sseBody != nil {
		_ = c.sseBody.Close()
	}
	c.failPendingRequests()
}

func (c *mcpClient) handleMessage(data string) {
	var msg struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal([]byte(data), &msg); err != nil || len(msg.ID) == 0 {
		return
	}

	var parsedID int64
	if err := json.Unmarshal(msg.ID, &parsedID); err != nil {
		var idStr string
		if json.Unmarshal(msg.ID, &idStr) != nil {
			return
		}
		val, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return
		}
		parsedID = val
	}

	c.requestMu.Lock()
	if ch, exists := c.requests[parsedID]; exists {
		delete(c.requests, parsedID)
		select {
		case ch <- data:
		default:
		}
	}
	c.requestMu.Unlock()
}

func (c *mcpClient) prepareRPCRequest(method string, params interface{}) (int64, chan string, []byte, error) {
	c.requestMu.Lock()
	if c.closed {
		c.requestMu.Unlock()
		return 0, nil, nil, fmt.Errorf("MCP server '%s' connection is closed", c.name)
	}
	id := c.nextID
	c.nextID++
	ch := make(chan string, 1)
	c.requests[id] = ch
	c.requestMu.Unlock()

	reqMap := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		reqMap["params"] = params
	}

	data, err := json.Marshal(reqMap)
	if err != nil {
		c.clearPendingRequest(id)
		return id, nil, nil, err
	}
	return id, ch, data, nil
}

func (c *mcpClient) clearPendingRequest(id int64) {
	c.requestMu.Lock()
	delete(c.requests, id)
	c.requestMu.Unlock()
}

func (c *mcpClient) awaitRPCResponse(ctx context.Context, id int64, ch <-chan string, closedFmt, timeoutMsg string, method string) (string, error) {
	select {
	case res, ok := <-ch:
		if !ok {
			return "", fmt.Errorf(closedFmt, method)
		}
		return res, nil
	case <-ctx.Done():
		c.clearPendingRequest(id)
		return "", fmt.Errorf("%s: %w", timeoutMsg, ctx.Err())
	}
}

func (c *mcpClient) requestWithTimeout(parent context.Context, method string, params interface{}, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	id, ch, data, err := c.prepareRPCRequest(method, params)
	if err != nil {
		return "", err
	}
	mcpLog("[MCP Tx] %s\n", string(data))

	if c.isStdio {
		if err := c.sendStdio(string(data)); err != nil {
			c.clearPendingRequest(id)
			return "", err
		}
		return c.awaitRPCResponse(
			ctx,
			id,
			ch,
			"stdio stream closed while waiting for '%s' response",
			fmt.Sprintf("request timeout waiting for '%s' response", method),
			method,
		)
	}

	resp, err := c.postJSON(ctx, data)
	if err != nil {
		c.clearPendingRequest(id)
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		c.clearPendingRequest(id)
		return "", fmt.Errorf("HTTP error %d", resp.StatusCode)
	}

	return c.awaitRPCResponse(ctx, id, ch, "MCP connection closed while waiting for '%s'", "request timeout or cancellation", method)
}

func (c *mcpClient) request(method string, params interface{}) (string, error) {
	return c.requestWithTimeout(context.Background(), method, params, limits.MCPRequestTimeout())
}

func (c *mcpClient) handshake() error {
	params := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]string{
			"name":    "loop-client",
			"version": "1.0.0",
		},
	}

	res, err := c.request("initialize", params)
	if err != nil {
		return err
	}
	if err := decodeRPCResponse(res, nil); err != nil {
		return err
	}

	data, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	mcpLog("[MCP Tx Notification] %s\n", string(data))

	if c.isStdio {
		if err := c.sendStdio(string(data)); err != nil {
			return fmt.Errorf("failed to send MCP initialized notification: %w", err)
		}
		c.initialized = true
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.postJSON(ctx, data)
	if err != nil {
		return fmt.Errorf("failed to send MCP initialized notification: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("MCP initialized notification returned HTTP %d", resp.StatusCode)
	}
	c.initialized = true
	return nil
}

func (c *mcpClient) listTools() ([]MCPTool, error) {
	if !c.initialized {
		return nil, fmt.Errorf("client not initialized")
	}

	const maxPages = 100
	var tools []MCPTool
	var cursor string
	seenCursors := make(map[string]bool)
	seenNames := make(map[string]bool)
	for page := 0; page < maxPages; page++ {
		var params interface{}
		if cursor != "" {
			params = map[string]string{"cursor": cursor}
		}
		res, err := c.request("tools/list", params)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch MCP tools page %d: %w", page+1, err)
		}
		var result struct {
			Tools      []MCPTool `json:"tools"`
			NextCursor string    `json:"nextCursor"`
		}
		if err := decodeRPCResponse(res, &result); err != nil {
			return nil, fmt.Errorf("invalid MCP tools page %d: %w", page+1, err)
		}
		for _, entry := range result.Tools {
			if seenNames[entry.Name] {
				return nil, fmt.Errorf("duplicate MCP tool name %q across pages", entry.Name)
			}
			seenNames[entry.Name] = true
			tools = append(tools, entry)
		}
		if result.NextCursor == "" {
			return tools, nil
		}
		if seenCursors[result.NextCursor] {
			return nil, fmt.Errorf("MCP tools/list returned a repeated cursor")
		}
		seenCursors[result.NextCursor] = true
		cursor = result.NextCursor
	}
	return nil, fmt.Errorf("MCP tools/list exceeded %d pages", maxPages)
}

func (c *mcpClient) callTool(ctx context.Context, name string, arguments string) (string, error) {
	if !c.initialized {
		return "", fmt.Errorf("client not initialized")
	}

	argsMap := make(map[string]interface{})
	if trimmed := strings.TrimSpace(arguments); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &argsMap); err != nil {
			return "", err
		}
	}

	res, err := c.requestWithTimeout(ctx, "tools/call", map[string]interface{}{
		"name":      name,
		"arguments": argsMap,
	}, limits.MCPRequestTimeout())
	if err != nil {
		return "", err
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := decodeRPCResponse(res, &result); err != nil {
		return "", err
	}

	var output []string
	for _, comp := range result.Content {
		if comp.Type == "text" {
			output = append(output, comp.Text)
		}
	}

	joined := strings.Join(output, "\n")
	if result.IsError {
		return joined, fmt.Errorf("tool execution failed")
	}
	return joined, nil
}
