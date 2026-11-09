package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"loop/pkg/config"
	"loop/pkg/runtime"
)

type rpcCommand struct {
	Arguments []string `json:"arguments"`
	ID        string   `json:"id"`
	Type      string   `json:"type"`
	Message   string   `json:"message"`
	Model     string   `json:"model"`
	Provider  string   `json:"provider"`
	Level     string   `json:"level"`
	SessionID string   `json:"session_id"`
	EntryID   string   `json:"entry_id"`
	Name      string   `json:"name"`
	Tools     []string `json:"tools"`
}

func serveRPC(ctx context.Context, options runtime.Options, input io.Reader, write func(any) error) (runErr error) {
	s, err := runtime.NewContext(ctx, options)
	if err != nil {
		return err
	}
	stopLifetime := context.AfterFunc(ctx, s.Agent.CancelLifetime)
	defer func() { stopLifetime() }()
	defer func() {
		if err := s.Close(context.Background()); err != nil && !errors.Is(runErr, err) {
			runErr = errors.Join(runErr, err)
		}
	}()
	input, stopInput := cancellableInput(ctx, input)
	defer stopInput()
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var command rpcCommand
		if err := json.Unmarshal(scanner.Bytes(), &command); err != nil {
			if err := write(map[string]any{"type": "response", "command": "parse", "success": false, "error": err.Error()}); err != nil {
				return err
			}
			continue
		}
		var data any
		var commandErr error
		switch command.Type {
		case "prompt":
			if s.Running() {
				commandErr = runtime.ErrBusy
			} else if command.Message == "" {
				commandErr = fmt.Errorf("prompt is empty")
			} else {
				_, commandErr = s.Start(ctx, command.Message)
				if commandErr != nil {
					break
				}
				data = map[string]string{"disposition": "started"}
				response := map[string]any{"type": "response", "id": command.ID, "command": command.Type, "success": true, "data": data}
				if err := write(response); err != nil {
					return err
				}
				continue
			}
		case "steer", "follow_up":
			commandErr = s.Queue(command.Message, command.Type == "follow_up")
		case "abort":
			commandErr = s.Abort(ctx)
		case "clear_queue":
			commandErr = s.ClearQueue()
		case "get_state":
			data = s.Status()
		case "get_messages":
			data = s.MessagesSnapshot()
		case "get_usage":
			if s.Store != nil {
				data, commandErr = s.Store.UsageContext(ctx, s.ID)
			} else {
				data = s.Usage()
			}
		case "get_models":
			data = s.Agent.Config.Models()
		case "discover_models":
			data, commandErr = s.Agent.DiscoverModels(ctx)
		case "get_commands":
			data = s.Commands()
		case "execute_command":
			data, commandErr = s.ExecuteCommand(ctx, command.Name, command.Arguments)
		case "get_tools":
			data = s.Agent.Registry.GetAvailableTools(s.Agent.ToolSelection)
		case "get_entries", "get_tree":
			if s.Store == nil {
				commandErr = fmt.Errorf("session is ephemeral")
			} else {
				data, commandErr = s.Store.EntriesContext(ctx, s.ID)
			}
		case "set_model", "set_thinking_level", "set_tools":
			if command.Type == "set_tools" {
				commandErr = s.SetTools(command.Tools)
			} else {
				cfg, err := s.Configuration()
				commandErr = err
				if err == nil {
					if command.Type == "set_model" {
						if command.Provider != "" {
							commandErr = cfg.ActivateProvider(command.Provider)
						}
						if command.Model != "" {
							cfg.Model = command.Model
						}
					} else {
						cfg.ReasoningEffort, commandErr = config.ParseReasoningEffort(command.Level)
					}
					if commandErr == nil {
						commandErr = s.UpdateConfig(cfg)
					}
				}
			}
		case "set_session_name":
			commandErr = s.SetName(ctx, command.Name)
		case "reload":
			commandErr = s.Reload()
		case "compact":
			commandErr = s.Compact(ctx)
		case "switch_session", "new_session", "fork":
			if s.Running() {
				commandErr = runtime.ErrBusy
				break
			}
			next := options
			next.Config, commandErr = s.Configuration()
			if commandErr != nil {
				break
			}
			next.SessionID = command.SessionID
			next.SessionName = ""
			next.Resume = false
			if command.Type == "new_session" {
				next.SessionID = ""
			}
			createdFork := ""
			if command.Type == "fork" {
				if s.Store == nil {
					commandErr = fmt.Errorf("session is ephemeral")
					break
				}
				next.SessionID, commandErr = s.Store.ForkSessionContext(ctx, s.ID, command.EntryID)
				if commandErr != nil {
					break
				}
				createdFork = next.SessionID
			}
			var replacement *runtime.Session
			replacement, commandErr = runtime.NewContext(ctx, next)
			if commandErr == nil {
				closeErr := s.Close(ctx)
				stopLifetime()
				s = replacement
				stopLifetime = context.AfterFunc(ctx, s.Agent.CancelLifetime)
				data = s.State()
				if closeErr != nil {
					commandErr = fmt.Errorf("switched to session %s, but closing the previous session failed: %w", s.ID, closeErr)
				}
			} else if createdFork != "" {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
				commandErr = errors.Join(commandErr, s.Store.ClearSessionContext(cleanupCtx, createdFork))
				cancel()
			}
		case "shutdown":
			closeErr := s.Close(ctx)
			response := map[string]any{"type": "response", "id": command.ID, "command": command.Type, "success": closeErr == nil}
			if closeErr != nil {
				response["error"] = closeErr.Error()
			}
			if err := write(response); err != nil {
				return errors.Join(closeErr, err)
			}
			return closeErr
		default:
			commandErr = fmt.Errorf("unknown RPC command %q", command.Type)
		}
		response := map[string]any{"type": "response", "id": command.ID, "command": command.Type, "success": commandErr == nil}
		if commandErr != nil {
			response["error"] = commandErr.Error()
		}
		if data != nil {
			response["data"] = data
		}
		if err := write(response); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return scanner.Err()
}
