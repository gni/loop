package interactive

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"unicode"
	"unicode/utf8"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/terminal"
	transporthttp "loop/pkg/transport/http"
	"loop/pkg/ui/style"
)

type MCPConfigActions struct {
	Reconnect     func(*config.Config) error
	Statuses      func() map[string]agent.MCPServerStatus
	Configuration func() (*config.Config, error)
}

type mcpManager struct {
	draft, saved *config.Config
	selected     int
	query        string
	searching    bool
	notice       string
	failed       bool
	busy         bool
	actions      MCPConfigActions
}

func RunInteractiveMCPConfig(cfg *config.Config, theme style.UITheme, input io.Reader, output io.Writer, actions MCPConfigActions) (*config.Config, error) {
	draft, err := config.Clone(cfg)
	if err != nil {
		return nil, err
	}
	if draft.MCPServers == nil {
		draft.MCPServers = make(map[string]config.MCPServerConfig)
	}
	manager := &mcpManager{draft: draft, saved: cfg, actions: actions}
	cancelled := false
	isTTY, err := withAltScreenTerminal(input, output, func() error {
		sr := NewSessionReader(input)
		defer sr.Close()
		signals := make(chan os.Signal, 1)
		terminal.NotifyResize(signals)
		defer signal.Stop(signals)
		for {
			names := manager.names()
			manager.selected = max(0, min(manager.selected, len(names)-1))
			width, height := terminal.GetDimensions()
			frame, page := renderMCPManager(manager, names, manager.statuses(), width, height, theme)
			if err := writeMenuFrame(output, frame); err != nil {
				return err
			}
			key, resized, err := sr.ReadKeyOrResize(signals)
			if err != nil {
				return err
			}
			if resized {
				continue
			}
			value := string(key)
			switch value {
			case "\x03", "\x04":
				cancelled = true
				return nil
			case "\x1b":
				if manager.searching || manager.query != "" {
					manager.searching, manager.query, manager.selected = false, "", 0
					continue
				}
				return nil
			}
			if width < mcpMinWidth || height < mcpMinHeight {
				continue
			}
			if manager.searching {
				switch value {
				case "\r", "\n":
					manager.searching = false
				case "\x7f", "\b":
					if manager.query != "" {
						_, size := utf8.DecodeLastRuneInString(manager.query)
						manager.query = manager.query[:len(manager.query)-size]
					}
				default:
					if len(key) > 0 && key[0] != 27 {
						for _, char := range value {
							if !unicode.IsControl(char) && len(manager.query) < 256 {
								manager.query += string(char)
							}
						}
					}
				}
				manager.selected = 0
				continue
			}
			switch value {
			case "/":
				manager.searching = true
			case "\x1b[A", "\x1bOA":
				if len(names) > 0 {
					manager.selected = (manager.selected - 1 + len(names)) % len(names)
				}
			case "\x1b[B", "\x1bOB":
				if len(names) > 0 {
					manager.selected = (manager.selected + 1) % len(names)
				}
			case "\x1b[5~":
				manager.selected = max(0, manager.selected-page)
			case "\x1b[6~":
				manager.selected = min(max(0, len(names)-1), manager.selected+page)
			case "\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[7~":
				manager.selected = 0
			case "\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~":
				manager.selected = max(0, len(names)-1)
			case "m", "M":
				manager.draft.MCP = !manager.draft.MCP
				manager.notice, manager.failed = "", false
			case " ":
				if len(names) > 0 {
					name := names[manager.selected]
					server := manager.draft.MCPServers[name]
					server.Enabled = !server.Enabled
					manager.draft.MCPServers[name] = server
					manager.notice, manager.failed = "", false
				}
			case "r", "R":
				if err := manager.reconnect(output, width, height, theme); err != nil {
					return err
				}
			case "a", "A":
				name, err := addMCPServer(manager.draft, sr, signals, output, theme)
				if err := manager.actionResult(err); err != nil {
					return err
				}
				if name != "" {
					manager.query = ""
					for index, entry := range manager.names() {
						if entry == name {
							manager.selected = index
						}
					}
					manager.notice = "Server added. Enter to configure."
				}
			case "d", "D":
				if len(names) > 0 {
					name := names[manager.selected]
					delete(manager.draft.MCPServers, name)
					manager.notice, manager.failed = "Server removed. Esc saves changes.", false
				}
			case "\r", "\n":
				if len(names) > 0 {
					err := editMCPServer(manager.draft, names[manager.selected], sr, output, theme)
					if err := manager.actionResult(err); err != nil {
						return err
					}
				}
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if cancelled {
		return nil, nil
	}
	if !isTTY {
		return cfg, nil
	}
	return draft, nil
}

func (m *mcpManager) actionResult(err error) error {
	var inputErr *menuInputError
	if errors.As(err, &inputErr) && !errors.Is(err, ErrSelectionCancelled) {
		return err
	}
	m.notice, m.failed = "", false
	if err != nil && !errors.Is(err, ErrSelectionCancelled) {
		m.notice, m.failed = menuDisplayText(transporthttp.RedactDiagnostic(err.Error())), true
	}
	return nil
}

func (m *mcpManager) statuses() map[string]agent.MCPServerStatus {
	if m.actions.Statuses != nil {
		return m.actions.Statuses()
	}
	return nil
}

func (m *mcpManager) reconnect(output io.Writer, width, height int, theme style.UITheme) error {
	m.failed = true
	if !m.draft.MCP {
		m.notice = "Enable MCP with M before reconnecting."
		return nil
	}
	enabled := 0
	for _, server := range m.draft.MCPServers {
		if server.Enabled {
			enabled++
		}
	}
	if enabled == 0 || m.actions.Reconnect == nil {
		m.notice = "Enable a server before reconnecting."
		if m.actions.Reconnect == nil {
			m.notice = "Reconnect is unavailable."
		}
		return nil
	}
	m.busy, m.failed, m.notice = true, false, "Reconnecting enabled servers..."
	frame, _ := renderMCPManager(m, m.names(), m.statuses(), width, height, theme)
	if err := writeMenuFrame(output, frame); err != nil {
		m.busy = false
		return err
	}
	draft, err := config.Clone(m.draft)
	if err == nil {
		err = m.actions.Reconnect(draft)
	}
	m.busy = false
	m.failed = err != nil
	m.notice = fmt.Sprintf("Reconnected %d enabled server(s).", enabled)
	if err != nil {
		m.notice = menuDisplayText(transporthttp.RedactDiagnostic(err.Error()))
	}
	if m.actions.Configuration != nil {
		if saved, snapshotErr := m.actions.Configuration(); snapshotErr == nil {
			m.saved = saved
		}
	} else if err == nil {
		m.saved, _ = config.Clone(m.draft)
	}
	return nil
}
