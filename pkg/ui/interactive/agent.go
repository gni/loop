package interactive

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"loop/pkg/agent/swarm"
	"loop/pkg/terminal"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func RunInteractiveAgentManager(mam *swarm.MultiAgentManager, theme style.UITheme, rlInput io.Reader, rlOutput io.Writer) error {
	isTTY, err := withAltScreenTerminal(rlInput, rlOutput, func() error {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGWINCH)
		defer signal.Stop(sigChan)

		sr := NewSessionReader(rlInput)
		defer sr.Close()

		selectedIdx := 0

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

			var buf strings.Builder
			buf.WriteString("\x1b[H")

			headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
			keyStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)
			valStyle := style.NewStyle().Foreground(theme.Text)
			highlightStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)

			buf.WriteString(headerStyle.Render("agent swarm manager") + "\n\n")

			leftLines := []string{
				headerStyle.Render("  active swarm nodes:"),
				style.NewStyle().Foreground(theme.Border).Render("  ───────────────────────────────────"),
			}

			for idx, name := range list {
				marker := "  "
				if name == activeName || (name == "base" && activeName == "") {
					marker = style.NewStyle().Foreground(theme.Success).Render("➔ ")
				}
				nameStr := valStyle.Render(name)
				if idx == selectedIdx {
					nameStr = headerStyle.Render(name)
				}
				leftLines = append(leftLines, fmt.Sprintf("   %s%s", marker, nameStr))
			}

			selectedName := list[selectedIdx]
			isFocused := selectedName == activeName || (selectedName == "base" && activeName == "")
			focusVal := valStyle.Render("No (press Enter to focus & chat)")
			if isFocused {
				focusVal = highlightStyle.Render("Yes (currently chatting)")
			}

			var sysPrompt, parentStr, skillStr, typeStr string
			if selectedName == "base" {
				typeStr = "Default Base Agent"
				parentStr = "none"
				skillStr = "Generic (all active skills)"
				sysPrompt = mam.BaseAgent.GetSystemPrompt()
			} else {
				typeStr = "Swarm Subagent"
				parentStr = mam.GetParentName(selectedName)
				if parentStr == "" {
					parentStr = "base"
				}
				skills, _ := mam.ListAgentSkills(selectedName)
				if len(skills) > 0 {
					names := make([]string, len(skills))
					for i, s := range skills {
						names[i] = s.Name
					}
					skillStr = strings.Join(names, ", ")
				} else {
					skillStr = "Generic"
				}
				sysPrompt = mam.GetAgentSystemPrompt(selectedName)
			}

			rightLines := []string{
				headerStyle.Render("selected node info:"),
				style.NewStyle().Foreground(theme.Border).Render("─────────────────────────────────────────"),
				fmt.Sprintf("  %s %s", keyStyle.Render("Name:"), valStyle.Render(selectedName)),
				fmt.Sprintf("  %s %s", keyStyle.Render("Type:"), valStyle.Render(typeStr)),
				fmt.Sprintf("  %s %s", keyStyle.Render("Parent:"), valStyle.Render(parentStr)),
				fmt.Sprintf("  %s %s", keyStyle.Render("Skill:"), valStyle.Render(skillStr)),
				fmt.Sprintf("  %s %s", keyStyle.Render("Focus:"), focusVal),
				fmt.Sprintf("  %s", keyStyle.Render("Instructions / Goal:")),
			}

			wrappedLines := render.WrapText(sysPrompt, 45)
			for i := 0; i < 8 && i < len(wrappedLines); i++ {
				rightLines = append(rightLines, "  "+valStyle.Render(wrappedLines[i]))
			}
			if len(wrappedLines) > 8 {
				rightLines = append(rightLines, "  "+style.NewStyle().Foreground(theme.Border).Italic(true).Render("... (truncated)"))
			}

			maxLines := len(leftLines)
			if len(rightLines) > maxLines {
				maxLines = len(rightLines)
			}

			width, _ := terminal.GetDimensions()
			if width >= 80 {
				for i := 0; i < maxLines; i++ {
					var leftPart, rightPart string
					if i < len(leftLines) {
						leftPart = leftLines[i]
					}
					if i < len(rightLines) {
						rightPart = rightLines[i]
					}
					leftLen := len(style.StripAnsi(leftPart))
					leftPad := ""
					if leftLen < 38 {
						leftPad = strings.Repeat(" ", 38-leftLen)
					}
					buf.WriteString(fmt.Sprintf("%s%s │ %s\n", leftPart, leftPad, rightPart))
				}
			} else {
				for _, line := range leftLines {
					buf.WriteString(line + "\n")
				}
				buf.WriteString(style.NewStyle().Foreground(theme.Border).Render("  ───────────────────────────────────\n"))
				for _, line := range rightLines {
					buf.WriteString(line + "\n")
				}
			}

			buf.WriteString("\n" + style.NewStyle().Foreground(theme.Border).Render("  controls: "))
			buf.WriteString(fmt.Sprintf("%s Focus & Chat   %s Create Agent   %s Delete Agent   %s Exit Swarm Manager\n\x1b[J",
				headerStyle.Render("[Enter]"),
				headerStyle.Render("[C]"),
				headerStyle.Render("[D]"),
				headerStyle.Render("[Esc]"),
			))

			outputStr := strings.ReplaceAll(buf.String(), "\n", "\x1b[K\r\n")
			_, _ = rlOutput.Write([]byte(outputStr))

			readBuf, resized, errReader := sr.ReadKeyOrResize(sigChan)
			if errReader != nil {
				return errReader
			}
			if resized {
				continue
			}
			n := len(readBuf)

			if n == 1 {
				switch readBuf[0] {
				case 3, 27, 4:
					return nil
				case 13, 10:
					mam.JoinAgent(selectedName)
					return nil
				case 'c', 'C':
					fmt.Fprint(rlOutput, "\x1b[?25h\x1b[H\x1b[2J=== Create New Swarm Agent ===\r\n\r\nEnter unique agent name (e.g. devops, tester): ")
					agentName, errInput := sr.ReadLine(rlOutput)
					if errInput == nil {
						agentName = strings.TrimSpace(agentName)
						if agentName == "" {
							fmt.Fprint(rlOutput, "\r\nError: Agent name cannot be empty. Press enter to continue...")
							_, _ = sr.ReadLine(rlOutput)
						} else {
							fmt.Fprint(rlOutput, "Enter system instructions / goals: ")
							sysPrompt, errInput2 := sr.ReadLine(rlOutput)
							if errInput2 == nil {
								sysPrompt = strings.TrimSpace(sysPrompt)
								if sysPrompt == "" {
									fmt.Fprint(rlOutput, "\r\nError: Instructions cannot be empty. Press enter to continue...")
									_, _ = sr.ReadLine(rlOutput)
								} else {
									fmt.Fprint(rlOutput, "\x1b[?25l")
									parentOptions := append([]string{"None (Base)"}, agentsList...)
									if parentIdx, errSelect := RunInteractiveSelect(sr, sigChan, rlOutput, "Select Parent Agent (default: None):", parentOptions, theme); errSelect == nil {
										parentName := ""
										if parentIdx > 0 {
											parentName = parentOptions[parentIdx]
										}
										skillOptions := []string{"Generic (All Active Skills)"}
										for _, s := range mam.BaseAgent.ActiveSkills {
											skillOptions = append(skillOptions, s.Name)
										}
										if skillIdx, errSelect2 := RunInteractiveSelect(sr, sigChan, rlOutput, "Select Dedicated Reference Skill:", skillOptions, theme); errSelect2 == nil {
											var skills []string
											if skillIdx > 0 {
												skills = []string{skillOptions[skillIdx]}
											}
											if errSpawn := mam.SpawnAgent(agentName, sysPrompt, parentName, skills); errSpawn != nil {
												fmt.Fprintf(rlOutput, "\x1b[?25h\x1b[H\x1b[2JError spawning agent: %v\r\n\r\nPress enter to continue...", errSpawn)
												_, _ = sr.ReadLine(rlOutput)
											} else {
												mam.JoinAgent(agentName)
												return nil
											}
										}
									}
								}
							}
						}
					}
					fmt.Fprint(rlOutput, "\x1b[?25l")
					continue

				case 'd', 'D':
					if selectedName == "base" {
						fmt.Fprint(rlOutput, "\x1b[?25h\x1b[H\x1b[2JError: Cannot delete default base agent.\r\n\r\nPress enter to continue...")
						_, _ = sr.ReadLine(rlOutput)
					} else {
						fmt.Fprintf(rlOutput, "\x1b[?25h\x1b[H\x1b[2JAre you sure you want to delete agent '%s'? [y/N]: ", selectedName)
						confirm, errInput := sr.ReadLine(rlOutput)
						if errInput == nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(confirm)), "y") {
							errKill := mam.RemoveAgent(selectedName)
							fmt.Fprint(rlOutput, "\x1b[H\x1b[2J")
							if errKill != nil {
								fmt.Fprintf(rlOutput, "Error terminating agent: %v\r\n\r\nPress enter to continue...", errKill)
							} else {
								fmt.Fprintf(rlOutput, "Agent '%s' terminated and deleted.\r\n\r\nPress enter to continue...", selectedName)
							}
							_, _ = sr.ReadLine(rlOutput)
						}
						if selectedIdx >= len(list)-1 {
							selectedIdx = len(list) - 2
							if selectedIdx < 0 {
								selectedIdx = 0
							}
						}
					}
					fmt.Fprint(rlOutput, "\x1b[?25l")
					continue
				}
			}

			if n >= 3 && readBuf[0] == 27 && readBuf[1] == '[' {
				switch readBuf[2] {
				case 'A':
					selectedIdx = (selectedIdx - 1 + len(list)) % len(list)
				case 'B':
					selectedIdx = (selectedIdx + 1) % len(list)
				}
			}
		}
	})
	if !isTTY {
		return fmt.Errorf("not a terminal")
	}
	return err
}