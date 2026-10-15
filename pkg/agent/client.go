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
	nextID         int64
	initialized    bool
	client         *http.Client
	cancel         context.CancelFunc
	connectTimeout time.Duration
	earlyResponses map[int64]string
}

type MCPTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
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
	for k, v := range c.config.Headers {
		req.Header.Set(k, v)
	}

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
		scanner := bufio.NewScanner(c.sseBody)
		var lastEvent string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event:") {
				lastEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			} else if strings.HasPrefix(line, "data:") {
				data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if lastEvent == "endpoint" {
					postURL := data
					if !strings.HasPrefix(postURL, "http://") && !strings.HasPrefix(postURL, "https://") {
						parsedBase, errBase := url.Parse(c.config.URL)
						parsedRel, errRel := url.Parse(postURL)
						if errBase == nil && errRel == nil {
							postURL = parsedBase.ResolveReference(parsedRel).String()
						}
					}
					mcpLog("[MCP] Resolved POST endpoint: %s\n", postURL)
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
	case postURL := <-endpointChan:
		c.postURL = postURL
	case <-time.After(c.connectTimeout):
		c.close()
		return fmt.Errorf("timeout waiting for SSE endpoint initialization (%s)", c.connectTimeout)
	}

	if err := c.handshake(); err != nil {
		c.close()
		return fmt.Errorf("handshake failed: %w", err)
	}
	return nil
}

func (c *mcpClient) close() {
	if c.stdin != nil {
		c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	if c.cancel != nil {
		c.cancel()
	}
	if c.sseBody != nil {
		c.sseBody.Close()
	}
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
	} else {
		if c.earlyResponses == nil {
			c.earlyResponses = make(map[int64]string)
		}
		c.earlyResponses[parsedID] = data
	}
	c.requestMu.Unlock()
}

func (c *mcpClient) prepareRPCRequest(method string, params interface{}) (int64, chan string, []byte, string, error) {
	c.requestMu.Lock()
	id := c.nextID
	c.nextID++
	if c.earlyResponses != nil {
		if early, exists := c.earlyResponses[id]; exists {
			delete(c.earlyResponses, id)
			c.requestMu.Unlock()
			return id, nil, nil, early, nil
		}
	}
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
		return id, nil, nil, "", err
	}
	return id, ch, data, "", nil
}

func (c *mcpClient) clearPendingRequest(id int64) {
	c.requestMu.Lock()
	delete(c.requests, id)
	c.requestMu.Unlock()
}

func (c *mcpClient) request(method string, params interface{}) (string, error) {
	if c.isStdio {
		return c.requestStdio(method, params, limits.MCPRequestTimeout())
	}

	id, ch, data, early, err := c.prepareRPCRequest(method, params)
	if err != nil || early != "" {
		return early, err
	}
	mcpLog("[MCP Tx] %s\n", string(data))

	ctx, cancel := context.WithTimeout(context.Background(), limits.MCPRequestTimeout())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", c.postURL, bytes.NewBuffer(data))
	if err != nil {
		c.clearPendingRequest(id)
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range c.config.Headers {
		req.Header.Set(k, v)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		c.clearPendingRequest(id)
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		c.clearPendingRequest(id)
		return "", fmt.Errorf("HTTP error %d", resp.StatusCode)
	}

	select {
	case res := <-ch:
		return res, nil
	case <-ctx.Done():
		c.clearPendingRequest(id)
		return "", fmt.Errorf("request timeout or cancellation")
	}
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

	var resp struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(res), &resp); err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("MCP error: %s", resp.Error.Message)
	}

	data, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	mcpLog("[MCP Tx Notification] %s\n", string(data))

	if c.isStdio {
		if err := c.sendStdio(string(data)); err != nil {
			mcpLog("[MCP] failed to send notifications/initialized: %v\n", err)
		}
		c.initialized = true
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if req, err := http.NewRequestWithContext(ctx, "POST", c.postURL, bytes.NewBuffer(data)); err == nil {
		req.Header.Set("Content-Type", "application/json")
		for k, v := range c.config.Headers {
			req.Header.Set(k, v)
		}
		if respDrop, errDrop := c.client.Do(req); errDrop == nil {
			respDrop.Body.Close()
		}
	}

	c.initialized = true
	return nil
}

func (c *mcpClient) listTools() ([]MCPTool, error) {
	if !c.initialized {
		return nil, fmt.Errorf("client not initialized")
	}

	res, err := c.request("tools/list", nil)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Result struct {
			Tools []MCPTool `json:"tools"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(res), &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("MCP error: %s", resp.Error.Message)
	}
	return resp.Result.Tools, nil
}

func (c *mcpClient) callTool(name string, arguments string) (string, error) {
	if !c.initialized {
		return "", fmt.Errorf("client not initialized")
	}

	var argsMap map[string]interface{}
	if err := json.Unmarshal([]byte(arguments), &argsMap); err != nil {
		return "", err
	}

	res, err := c.request("tools/call", map[string]interface{}{
		"name":      name,
		"arguments": argsMap,
	})
	if err != nil {
		return "", err
	}

	var resp struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(res), &resp); err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", fmt.Errorf("MCP error: %s", resp.Error.Message)
	}

	var output []string
	for _, comp := range resp.Result.Content {
		if comp.Type == "text" {
			output = append(output, comp.Text)
		}
	}

	joined := strings.Join(output, "\n")
	if resp.Result.IsError {
		return joined, fmt.Errorf("tool execution failed")
	}
	return joined, nil
}