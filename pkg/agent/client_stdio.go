package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// startStdio launches the configured command as a local MCP server over
// JSON-RPC on stdin/stdout (newline-delimited).
func (c *mcpClient) startStdio() error {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	cmd := exec.CommandContext(ctx, c.config.Command, c.config.Args...)
	cmd.Env = os.Environ()
	for k, v := range c.config.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start MCP server '%s': %w", c.name, err)
	}

	c.cmd = cmd
	c.stdin = stdin

	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || !strings.HasPrefix(line, "{") {
				continue
			}
			mcpLog("[MCP Rx] %s\n", line)
			c.handleMessage(line)
		}
		if err := scanner.Err(); err != nil {
			mcpLog("stdio stream error for '%s': %v\n", c.name, err)
		} else {
			mcpLog("stdio stream closed for '%s'\n", c.name)
		}
	}()

	if err := c.handshake(); err != nil {
		c.close()
		return fmt.Errorf("handshake failed: %w", err)
	}

	return nil
}

func (c *mcpClient) sendStdio(data string) error {
	if c.stdin == nil {
		return fmt.Errorf("stdio transport not started")
	}
	if _, err := io.WriteString(c.stdin, data+"\n"); err != nil {
		return err
	}
	if f, ok := c.stdin.(interface{ Flush() error }); ok {
		return f.Flush()
	}
	return nil
}

func (c *mcpClient) requestStdio(method string, params interface{}, timeout time.Duration) (string, error) {
	c.requestMu.Lock()
	id := c.nextID
	c.nextID++
	ch := make(chan string, 1)
	c.requests[id] = ch
	c.requestMu.Unlock()

	reqMap := map[string]interface{}{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		reqMap["params"] = params
	}

	data, err := json.Marshal(reqMap)
	if err != nil {
		c.requestMu.Lock()
		delete(c.requests, id)
		c.requestMu.Unlock()
		return "", err
	}

	mcpLog("[MCP Tx] %s\n", string(data))
	if err := c.sendStdio(string(data)); err != nil {
		c.requestMu.Lock()
		delete(c.requests, id)
		c.requestMu.Unlock()
		return "", err
	}

	select {
	case res := <-ch:
		return res, nil
	case <-time.After(timeout):
		c.requestMu.Lock()
		delete(c.requests, id)
		c.requestMu.Unlock()
		return "", fmt.Errorf("request timeout waiting for '%s' response", method)
	}
}
