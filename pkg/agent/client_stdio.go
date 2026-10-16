package agent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

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
		_ = stdin.Close()
		cancel()
		return err
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		cancel()
		return fmt.Errorf("failed to start MCP server '%s': %w", c.name, err)
	}

	c.cmd = cmd
	c.stdin = stdin

	go func() {
		defer c.failPendingRequests()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
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
	id, ch, data, early, err := c.prepareRPCRequest(method, params)
	if err != nil || early != "" {
		return early, err
	}

	mcpLog("[MCP Tx] %s\n", string(data))
	if err := c.sendStdio(string(data)); err != nil {
		c.clearPendingRequest(id)
		return "", err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case res, ok := <-ch:
		if !ok {
			return "", fmt.Errorf("stdio stream closed while waiting for '%s' response", method)
		}
		return res, nil
	case <-timer.C:
		c.clearPendingRequest(id)
		return "", fmt.Errorf("request timeout waiting for '%s' response", method)
	}
}