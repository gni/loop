package interactive

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	"loop/pkg/agent/swarm"
	"loop/pkg/terminal"
	"loop/pkg/ui/screen"
	"loop/pkg/ui/style"
)

func RunInteractiveAgentManager(mam *swarm.MultiAgentManager, theme style.UITheme, rlInput io.Reader, rlOutput io.Writer) error {
	if mam == nil || mam.BaseAgent == nil {
		return fmt.Errorf("agent manager is unavailable")
	}
	isTTY, err := withAltScreenTerminal(rlInput, rlOutput, func() error {
		sigChan := make(chan os.Signal, 1)
		terminal.NotifyResize(sigChan)
		defer signal.Stop(sigChan)

		sr := NewSessionReader(rlInput)
		defer sr.Close()

		comp := screen.NewCompositor(80, 24)
		notice := ""
		selectedIdx := 0
		for i, name := range mam.ListAgents() {
			if name == mam.ActiveAgentName() {
				selectedIdx = i + 1
				break
			}
		}

		for {
			agentsList := mam.ListAgents()
			activeName := mam.ActiveAgentName()
			list := append([]string{"base"}, agentsList...)

			if selectedIdx >= len(list) {
				selectedIdx = len(list) - 1
			}
			if selectedIdx < 0 {
				selectedIdx = 0
			}

			selectedName := list[selectedIdx]
			width, height := terminal.GetDimensions()
			if width <= 0 {
				width = 80
			}
			if height <= 0 {
				height = 24
			}
			rows := renderAgentManagerRows(mam, list, activeName, selectedIdx, width, height, theme, notice)
			if err := flushMenuScreen(comp, rlOutput, width, height, rows, screen.FlushOptions{}); err != nil {
				return err
			}

			readBuf, resized, errReader := sr.ReadKeyOrResize(sigChan)
			if errReader != nil {
				return errReader
			}
			if resized {
				terminal.RefreshDimensions()
				continue
			}

			switch string(readBuf) {
			case "\x03", "\x1b", "\x04":
				return nil
			case "\r", "\n":
				if mam.JoinAgent(selectedName) {
					return nil
				}
				continue
			case "c", "C":
				created, err := createManagedAgent(mam, sr, sigChan, rlOutput, theme)
				comp.Invalidate()
				var inputErr *menuInputError
				if errors.As(err, &inputErr) && !errors.Is(err, ErrSelectionCancelled) {
					return err
				}
				if created {
					return nil
				}
				notice = agentActionNotice(err)
				continue
			case "d", "D":
				err := deleteManagedAgent(mam, selectedName, sr, sigChan, rlOutput, theme)
				comp.Invalidate()
				var inputErr *menuInputError
				if errors.As(err, &inputErr) && !errors.Is(err, ErrSelectionCancelled) {
					return err
				}
				notice = agentActionNotice(err)
				continue
			case "\x1b[A", "\x1bOA":
				selectedIdx = (selectedIdx - 1 + len(list)) % len(list)
			case "\x1b[B", "\x1bOB":
				selectedIdx = (selectedIdx + 1) % len(list)
			}
		}
	})
	if !isTTY {
		return fmt.Errorf("not a terminal")
	}
	return err
}
