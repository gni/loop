package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"loop/pkg/config"
	"loop/pkg/domain/message"
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

func serveRPC(ctx context.Context, options runtime.Options, input io.Reader, write func(any) error) error {
	s, err := runtime.New(options)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close(context.Background()) }()
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
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
			if s.State().Running {
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
			s.ClearQueue()
		case "get_state":
			data = s.State()
		case "get_messages":
			data = s.State().Messages
		case "get_usage":
			if s.Store != nil {
				data, commandErr = s.Store.Usage(s.ID)
			} else {
				data = message.TotalUsage(s.State().Messages)
			}
		case "get_models":
			data = s.Agent.Config.Models()
		case "discover_models":
			data, commandErr = s.Agent.DiscoverModels(ctx)
		case "get_commands":
 data=s.Commands()
		case "execute_command":
 data,commandErr=s.ExecuteCommand(ctx,command.Name,command.Arguments)
		case "get_tools":
			data = s.Agent.Registry.GetAvailableTools(s.Agent.ToolSelection)
		case "get_entries", "get_tree":
			if s.Store == nil {
				commandErr = fmt.Errorf("session is ephemeral")
			} else {
				data, commandErr = s.Store.Entries(s.ID)
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
			if s.Store == nil {
				commandErr = fmt.Errorf("session is ephemeral")
			} else {
				commandErr = s.Store.SetName(s.ID, command.Name)
			}
		case "reload":
			commandErr = s.Reload()
		case "compact":
			commandErr = s.Compact(ctx)
		case "switch_session", "new_session", "fork":
			if s.State().Running {
				commandErr = runtime.ErrBusy
				break
			}
			next := options
 next.Config,commandErr=s.Configuration()
 if commandErr!=nil{break}
			next.SessionID = command.SessionID
			next.Resume = false
			if command.Type == "new_session" {
				next.SessionID = ""
			}
			if command.Type == "fork" {
				if s.Store == nil {
					commandErr = fmt.Errorf("session is ephemeral")
					break
				}
				next.SessionID, commandErr = s.Store.ForkSession(s.ID, command.EntryID)
				if commandErr != nil {
					break
				}
			}
			var replacement *runtime.Session
			replacement, commandErr = runtime.New(next)
			if commandErr == nil {
				_ = s.Close(ctx)
				s = replacement
				data = s.State()
			}
		case "shutdown":
			if err := write(map[string]any{"type": "response", "id": command.ID, "command": command.Type, "success": true}); err != nil {
				return err
			}
			return nil
		default:
			commandErr = fmt.Errorf("unknown RPC command %q", command.Type)
		}
		response := map[string]any{"type": "response", "id": command.ID, "command": command.Type, "success": commandErr == nil}
		if commandErr != nil {
			response["error"] = commandErr.Error()
		} else if data != nil {
			response["data"] = data
		}
		if err := write(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}
