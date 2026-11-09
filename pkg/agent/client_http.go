package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"loop/pkg/domain/limits"
	"loop/pkg/transport/sse"
	"net/http"
	"strings"
	"time"
)

func (c *mcpClient) startHTTP() error {
	c.ctx, c.cancel = context.WithCancel(c.context())
	c.isHTTP = true
	c.postURL = c.config.URL
	return c.completeHandshake()
}

func (c *mcpClient) readHTTPResponse(ctx context.Context, response *http.Response, id int64, result <-chan string) (string, error) {
	defer c.clearPendingRequest(id)
	if sessionID := response.Header.Get("Mcp-Session-Id"); sessionID != "" {
		c.requestMu.Lock()
		c.sessionID = sessionID
		c.requestMu.Unlock()
	}
	if strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		data, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
		if err != nil {
			return "", err
		}
		if len(data) > 8<<20 {
			return "", fmt.Errorf("MCP response exceeds 8 MiB")
		}
		c.handleMessage(string(data))
	} else if strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		reader := sse.NewReader(response.Body)
		for {
			event, err := reader.Read(ctx)
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", err
			}
			c.handleMessage(event.Data)
			select {
			case value, ok := <-result:
				if !ok {
					return "", fmt.Errorf("MCP connection closed")
				}
				return value, nil
			default:
			}
		}
	} else {
		return "", fmt.Errorf("unsupported MCP content type %q", response.Header.Get("Content-Type"))
	}
	select {
	case value, ok := <-result:
		if !ok {
			return "", fmt.Errorf("MCP connection closed")
		}
		return value, nil
	default:
		return "", fmt.Errorf("MCP response omitted request %d", id)
	}
}

func (c *mcpClient) resourceRequest(ctx context.Context, method string, params any) (json.RawMessage, error) {
	response, err := c.requestWithTimeout(ctx, method, params, c.requestTimeout())
	if err != nil {
		return nil, err
	}
	var result json.RawMessage
	if err := decodeRPCResponse(response, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *mcpClient) requestTimeout() time.Duration {
	if c.timeout > 0 {
		return c.timeout
	}
	return limits.MCPRequestTimeout()
}
