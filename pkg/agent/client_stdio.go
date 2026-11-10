package agent

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"loop/pkg/process"
)

func (c *mcpClient) startStdio() error {
	ctx, cancel := c.context(), c.cancel

	cmd := exec.CommandContext(ctx, c.config.Command, c.config.Args...)
	process.SetupProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = os.Environ()
	for k, v := range c.config.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return err
	}
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		cancel()
		return err
	}
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		cancel()
		return err
	}
	cmd.Stdout, cmd.Stderr = stdoutWriter, stderrWriter

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		_ = stderr.Close()
		_ = stderrWriter.Close()
		cancel()
		return fmt.Errorf("failed to start MCP server '%s': %w", c.name, err)
	}
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()

	c.cmd = cmd
	c.processDone = make(chan struct{})
	c.stdin = stdin

	go func() {
		defer stderr.Close()
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 0, 16*1024), 1024*1024)
		for scanner.Scan() {
			mcpLog("[MCP Stderr %s] bytes=%d [content omitted]\n", c.name, len(scanner.Bytes()))
		}
	}()

	stdoutDone := make(chan struct{})
	go func() {
		defer close(stdoutDone)
		defer stdout.Close()
		defer c.failPendingRequests()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || !strings.HasPrefix(line, "{") {
				continue
			}
			mcpLog("[MCP Rx] bytes=%d [payload omitted]\n", len(line))
			c.handleMessage(line)
		}
		if err := scanner.Err(); err != nil {
			mcpLog("stdio stream error for '%s': %v\n", c.name, err)
		} else {
			mcpLog("stdio stream closed for '%s'\n", c.name)
		}
	}()
	go func() {
		// Observe the command independently of inherited output pipes. A child
		// holding stdout open must not hide a dead server or survive its exit.
		_ = cmd.Wait()
		_ = process.KillProcessGroup(cmd)
		select {
		case <-stdoutDone:
		case <-time.After(cmd.WaitDelay):
			_ = stdout.Close()
			<-stdoutDone
		}
		_ = stderr.Close()
		close(c.processDone)
	}()

	return c.completeHandshake()
}

func (c *mcpClient) sendStdio(data string) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
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
