package interactive

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"

	"loop/pkg/agent/swarm"
	"loop/pkg/terminal"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func RunInteractiveAgentManager(mam *swarm.MultiAgentManager, theme style.UITheme, rlInput io.Reader, rlOutput io.Writer) error {
	var fd int
	if f, ok := rlInput.(*os.File); ok {
		fd = int(f.Fd())
	} else {
		fd = int(os.Stdin.Fd())
	}

	if !term.IsTerminal(fd) {
		return fmt.Errorf("not a terminal")
	}

	initialState, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer term.Restore(fd, initialState)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGWINCH)
	defer signal.Stop(sigChan)

	sr := NewSessionReader(rlInput)
	defer sr.Close()

	fmt.Fprint(rlOutput, terminal.EnterAlternateScreen)
	fmt.Fprint(rlOutput, "\x1b[?25l")
	defer func() {
		fmt.Fprint(rlOutput, "\x1b[?25h")
		fmt.Fprint(rlOutput, terminal.ExitAlternateScreen)
	}()

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

		titleStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		buf.WriteString(titleStyle.Render("agent swarm manager"))
		buf.WriteString("\n\n")

		headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		keyStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)
		valStyle := style.NewStyle().Foreground(theme.Text)
		highlightStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)

		var leftLines []string
		leftLines = append(leftLines, headerStyle.Render("  active swarm nodes:"))
		leftLines = append(leftLines, style.NewStyle().Foreground(theme.Border).Render("  ───────────────────────────────────"))

		for idx, name := range list {
			marker := "  "
			if name == activeName || (name == "base" && activeName == "") {
				marker = style.NewStyle().Foreground(theme.Success).Render("➔ ")
			}

			var nameStr string
			if idx == selectedIdx {
				nameStr = style.NewStyle().Foreground(theme.Primary).Bold(true).Render(name)
			} else {
				nameStr = style.NewStyle().Foreground(theme.Text).Render(name)
			}

			leftLines = append(leftLines, fmt.Sprintf("   %s%s", marker, nameStr))
		}

		selectedName := list[selectedIdx]
		var sysPrompt string
		var parentStr string
		var skillStr string
		var typeStr string

		isFocused := (selectedName == activeName)
		if selectedName == "base" && activeName == "" {
			isFocused = true
		}
		focusVal := valStyle.Render("No (press Enter to focus & chat)")
		if isFocused {
			focusVal = highlightStyle.Render("Yes (currently chatting)")
		}

		if selectedName == "base" {
			typeStr = "Default Base Agent"
			parentStr = "none"
			skillStr = "Generic (all active skills)"
			sysPrompt = mam.BaseAgent.GetSystemPrompt()
		} else {
			typeStr = "Swarm Subagent"
			parentName := mam.GetParentName(selectedName)
			if parentName == "" {
				parentStr = "base"
			} else {
				parentStr = parentName
			}

			skills, _ := mam.ListAgentSkills(selectedName)
			var skillNames []string
			for _, s := range skills {
				skillNames = append(skillNames, s.Name)
			}
			if len(skillNames) > 0 {
				skillStr = strings.Join(skillNames, ", ")
			} else {
				skillStr = "Generic"
			}
			sysPrompt = mam.GetAgentSystemPrompt(selectedName)
		}

		var rightLines []string
		rightLines = append(rightLines, headerStyle.Render("selected node info:"))
		rightLines = append(rightLines, style.NewStyle().Foreground(theme.Border).Render("─────────────────────────────────────────"))
		rightLines = append(rightLines, fmt.Sprintf("  %s %s", keyStyle.Render("Name:"), valStyle.Render(selectedName)))
		rightLines = append(rightLines, fmt.Sprintf("  %s %s", keyStyle.Render("Type:"), valStyle.Render(typeStr)))
		rightLines = append(rightLines, fmt.Sprintf("  %s %s", keyStyle.Render("Parent:"), valStyle.Render(parentStr)))
		rightLines = append(rightLines, fmt.Sprintf("  %s %s", keyStyle.Render("Skill:"), valStyle.Render(skillStr)))
		rightLines = append(rightLines, fmt.Sprintf("  %s %s", keyStyle.Render("Focus:"), focusVal))
		rightLines = append(rightLines, fmt.Sprintf("  %s", keyStyle.Render("Instructions / Goal:")))

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
		useSideBySide := width >= 80

		if useSideBySide {
			for i := 0; i < maxLines; i++ {
				var leftPart string
				if i < len(leftLines) {
					leftPart = leftLines[i]
				}
				leftLen := len(style.StripAnsi(leftPart))
				leftPad := ""
				if leftLen < 38 {
					leftPad = strings.Repeat(" ", 38-leftLen)
				}

				var rightPart string
				if i < len(rightLines) {
					rightPart = rightLines[i]
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

		navStyle := style.NewStyle().Foreground(theme.Border)
		keyStyleBar := style.NewStyle().Foreground(theme.Primary).Bold(true)
		buf.WriteString("\n")
		buf.WriteString(navStyle.Render("  controls: "))
		buf.WriteString(fmt.Sprintf("%s Focus & Chat   ", keyStyleBar.Render("[Enter]")))
		buf.WriteString(fmt.Sprintf("%s Create Agent   ", keyStyleBar.Render("[C]")))
		buf.WriteString(fmt.Sprintf("%s Delete Agent   ", keyStyleBar.Render("[D]")))
		buf.WriteString(fmt.Sprintf("%s Exit Swarm Manager\n", keyStyleBar.Render("[Esc]")))

		buf.WriteString("\x1b[J")

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
			char := readBuf[0]

			if char == 3 || char == 27 || char == 4 {
				return nil
			}

			if char == 13 || char == 10 {
				mam.JoinAgent(selectedName)
				return nil
			}

			if char == 'c' || char == 'C' {
				fmt.Fprint(rlOutput, "\x1b[?25h\x1b[H\x1b[2J")

				fmt.Fprint(rlOutput, "=== Create New Swarm Agent ===\r\n\r\n")

				fmt.Fprint(rlOutput, "Enter unique agent name (e.g. devops, tester): ")
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
								parentIdx, errSelect := RunInteractiveSelect(sr, sigChan, rlOutput, "Select Parent Agent (default: None):", parentOptions, theme)
								if errSelect == nil {
									parentName := ""
									if parentIdx > 0 {
										parentName = parentOptions[parentIdx]
									}

									skillOptions := []string{"Generic (All Active Skills)"}
									for _, s := range mam.BaseAgent.ActiveSkills {
										skillOptions = append(skillOptions, s.Name)
									}
									skillIdx, errSelect2 := RunInteractiveSelect(sr, sigChan, rlOutput, "Select Dedicated Reference Skill:", skillOptions, theme)
									if errSelect2 == nil {
										skillName := ""
										if skillIdx > 0 {
											skillName = skillOptions[skillIdx]
										}

										var skills []string
										if skillName != "" {
											skills = []string{skillName}
										}

										errSpawn := mam.SpawnAgent(agentName, sysPrompt, parentName, skills)
										if errSpawn != nil {
											fmt.Fprint(rlOutput, "\x1b[?25h\x1b[H\x1b[2J")
											fmt.Fprintf(rlOutput, "Error spawning agent: %v\r\n\r\nPress enter to continue...", errSpawn)
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
			}

			if char == 'd' || char == 'D' {
				if selectedName == "base" {
					fmt.Fprint(rlOutput, "\x1b[?25h\x1b[H\x1b[2J")
					fmt.Fprint(rlOutput, "Error: Cannot delete default base agent.\r\n\r\nPress enter to continue...")
					_, _ = sr.ReadLine(rlOutput)
					fmt.Fprint(rlOutput, "\x1b[?25l")
				} else {
					fmt.Fprint(rlOutput, "\x1b[?25h\x1b[H\x1b[2J")
					fmt.Fprintf(rlOutput, "Are you sure you want to delete agent '%s'? [y/N]: ", selectedName)

					confirm, errInput := sr.ReadLine(rlOutput)
					if errInput == nil && (strings.HasPrefix(strings.ToLower(strings.TrimSpace(confirm)), "y")) {
						errKill := mam.RemoveAgent(selectedName)
						fmt.Fprint(rlOutput, "\x1b[H\x1b[2J")
						if errKill != nil {
							fmt.Fprintf(rlOutput, "Error terminating agent: %v\r\n\r\nPress enter to continue...", errKill)
						} else {
							fmt.Fprintf(rlOutput, "Agent '%s' terminated and deleted.\r\n\r\nPress enter to continue...", selectedName)
						}
						_, _ = sr.ReadLine(rlOutput)
					}

					fmt.Fprint(rlOutput, "\x1b[?25l")

					if selectedIdx >= len(list)-1 {
						selectedIdx = len(list) - 2
						if selectedIdx < 0 {
							selectedIdx = 0
						}
					}
				}
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
}
