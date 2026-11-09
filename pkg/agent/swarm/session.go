package swarm

import (
	"io"

	"loop/pkg/ui/style"
)

func (m *MultiAgentManager) SetOutput(writer io.Writer, theme style.UITheme) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.w = writer
	m.theme = theme
}

func (m *MultiAgentManager) SwitchSessionDirectory(directory string, ephemeral bool) error {
	prepared, err := m.PrepareSessionDirectory(directory, ephemeral)
	if err != nil {
		return err
	}
	defer prepared.Close()
	return prepared.Commit()
}
