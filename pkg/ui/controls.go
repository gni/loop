package ui

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/ui/style"
)

// GetPromptSymbol returns the standard interactive console input prompt glyph.
func GetPromptSymbol(cfg *config.Config) string {
	return "> "
}

// DrawConsoleStaticControlsLocked renders fixed bottom console controls while holding TerminalMu.
func DrawConsoleStaticControlsLocked(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal, drawPrompt bool) {
	termW, height := GetTerminalSize()
	if height <= 3 {
		return
	}
	if termW <= 0 {
		termW = 80
	}
	activeTheme := GetConfiguredTheme(a.Config)

	promptPrefix := GetPromptSymbol(a.Config)
	if kiReader != nil && kiReader.MAM != nil && kiReader.MAM.ActiveAgent != nil {
		promptPrefix = fmt.Sprintf("[%s]%s", kiReader.MAM.ActiveAgent.Name, promptPrefix)
	}

	qLen := 0
	if kiReader != nil {
		qLen = kiReader.QueueLen()
	}
	promptStr, fullPrefixPlain := style.FormatPromptWithQueue(activeTheme, promptPrefix, qLen)

	getUI().StateMu.Lock()
	inApproval := getUI().InApprovalPrompt
	getUI().StateMu.Unlock()

	inputLine := ""
	posOffset := 0
	if drawPrompt {
		getUI().StateMu.Lock()
		activeOp := getUI().ActiveCancelFunc != nil
		getUI().StateMu.Unlock()
		if activeOp && kiReader != nil {
			getUI().StateMu.Lock()
			inputLine = string(kiReader.TypeAheadBuffer)
			posOffset = len([]rune(inputLine))
			getUI().StateMu.Unlock()
		} else if rl != nil {
			inputLine, posOffset = getTerminalLine(rl)
		} else if kiReader != nil {
			inputLine = kiReader.CurrentInputLine
			posOffset = kiReader.CurrentInputPos
		}
		if kiReader != nil && kiReader.PastedText != "" {
			inputLine = inputLine + kiReader.PastedText
			posOffset = len([]rune(inputLine))
		}
	}

	inputLine = strings.ReplaceAll(inputLine, "\t", " ")
	inputLine = strings.ReplaceAll(inputLine, "\r", "")
	inputLine, posOffset = NormalizeHistoryInput(inputLine, posOffset)

	cleanInput := strings.TrimRight(inputLine, "\r\n")
	logicalLines := strings.Split(cleanInput, "\n")
	hasMultipleLines := len(logicalLines) > 1

	prefixLen := utf8.RuneCountInString(stripAnsi(fullPrefixPlain))
	availWidth := termW - prefixLen - 1
	if availWidth < 10 {
		availWidth = 10
	}

	type visualRow struct {
		text    string
		isFirst bool
	}

	var vRows []visualRow
	cursorVRow := 0
	cursorVCol := 1 + prefixLen
	cursorFound := false
	accumRunes := 0

	if hasMultipleLines {
		for lIdx, lStr := range logicalLines {
			runes := []rune(lStr)
			if len(runes) == 0 {
				if !cursorFound && posOffset <= accumRunes {
					cursorVRow = len(vRows)
					cursorVCol = 1 + prefixLen
					cursorFound = true
				}
				vRows = append(vRows, visualRow{text: "", isFirst: lIdx == 0})
				accumRunes++ // accounting for '\n'
				continue
			}

			for start := 0; start < len(runes); start += availWidth {
				end := start + availWidth
				if end > len(runes) {
					end = len(runes)
				}
				chunkRunes := runes[start:end]
				chunkLen := len(chunkRunes)

				if !cursorFound && posOffset <= accumRunes+chunkLen {
					cursorVRow = len(vRows)
					colOffset := posOffset - accumRunes
					cursorVCol = 1 + prefixLen + colOffset
					cursorFound = true
				}

				vRows = append(vRows, visualRow{
					text:    string(chunkRunes),
					isFirst: lIdx == 0 && start == 0,
				})
				accumRunes += chunkLen
			}
			accumRunes++ // accounting for '\n'
		}

		if !cursorFound {
			if len(vRows) > 0 {
				cursorVRow = len(vRows) - 1
				cursorVCol = 1 + prefixLen + len([]rune(vRows[len(vRows)-1].text))
			}
		}
	}

	effectiveOffset := 0
	if hasMultipleLines {
		maxAllowedOffset := height - 10
		if maxAllowedOffset > 15 {
			maxAllowedOffset = 15
		}
		if maxAllowedOffset < 2 {
			maxAllowedOffset = 2
		}
		effectiveOffset = len(vRows) - 1
		if effectiveOffset > maxAllowedOffset {
			effectiveOffset = maxAllowedOffset
		}
	}

	getUI().StateMu.Lock()
	oldOffset := getUI().PasteLinesOffset
	getUI().PasteLinesOffset = effectiveOffset
	getUI().StateMu.Unlock()

	delta := effectiveOffset - oldOffset
	offset := getUI().ScrollRegionOffset
	oldScrollBottom := height - 2 - offset - oldOffset
	if oldScrollBottom < 1 {
		oldScrollBottom = 1
	}

	maxOffset := oldOffset
	if effectiveOffset > maxOffset {
		maxOffset = effectiveOffset
	}

	promptStartRow := height - 2 - effectiveOffset
	if promptStartRow < 1 {
		promptStartRow = 1
	}

	var frameBuf bytes.Buffer
	// Hide cursor while redrawing static controls and prompt to eliminate any cursor flicker/jumping onto column 1 ('>')
	frameBuf.WriteString("\x1b[?25l")

	if delta > 0 && oldScrollBottom > delta {
		fmt.Fprintf(&frameBuf, "\x1b7\x1b[1;%dr\x1b[%d;1H\x1b[%dS\x1b8", oldScrollBottom, oldScrollBottom, delta)
		if a != nil {
			if uiImpl, ok := a.UI.(*AgentUIImpl); ok && uiImpl.PPWriter != nil {
				uiImpl.PPWriter.AdjustPrintLineLocked(-delta)
			} else if a.CurrentWriter != nil {
				if pw, ok := a.CurrentWriter.(*PromptPreservingWriter); ok {
					pw.AdjustPrintLineLocked(-delta)
				}
			}
		}
	}

	for l := height - 4 - maxOffset; l <= height-2; l++ {
		if l >= 1 {
			fmt.Fprintf(&frameBuf, "\x1b[%d;1H\x1b[2K", l)
		}
	}

	// Draw Status Bar at row height
	DrawStatusBarLocked(&frameBuf, activeTheme)

	// Draw Separator at row height-3-PasteLinesOffset
	DrawStaticPromptSeparatorLocked(&frameBuf, a.Config.ShowThinking, a.Config.ReasoningEffort, activeTheme)

	// Draw Stats Line at row height-4-PasteLinesOffset
	getUI().StateMu.Lock()
	savedStats := getUI().LastStatsText
	getUI().StateMu.Unlock()
	DrawStaticStatsLineLocked(&frameBuf, activeTheme, "", savedStats)

	if inApproval {
		fmt.Fprintf(&frameBuf, "\x1b[%d;1H\x1b[2K", promptStartRow)
	} else {
		if hasMultipleLines && len(vRows) > 0 {
			indent := strings.Repeat(" ", prefixLen)
			numVisibleRows := effectiveOffset + 1
			if numVisibleRows < 1 {
				numVisibleRows = 1
			}

			startVRow := 0
			if len(vRows) > numVisibleRows {
				if cursorVRow >= startVRow+numVisibleRows {
					startVRow = cursorVRow - numVisibleRows + 1
				}
				if cursorVRow < startVRow {
					startVRow = cursorVRow
				}
				if startVRow < 0 {
					startVRow = 0
				}
				if startVRow+numVisibleRows > len(vRows) {
					startVRow = len(vRows) - numVisibleRows
				}
				if startVRow < 0 {
					startVRow = 0
				}
			}

			for r := 0; r < numVisibleRows; r++ {
				vIdx := startVRow + r
				if vIdx >= len(vRows) {
					break
				}
				row := promptStartRow + r
				if row > height-2 {
					break
				}
				fmt.Fprintf(&frameBuf, "\x1b[%d;1H\x1b[2K", row)
				vr := vRows[vIdx]
				if vr.isFirst {
					frameBuf.WriteString(promptStr)
				} else {
					frameBuf.WriteString(indent)
				}
				frameBuf.WriteString(vr.text)
			}

			cursorRow := promptStartRow + (cursorVRow - startVRow)
			if cursorRow > height-2 {
				cursorRow = height - 2
			}
			if cursorRow < promptStartRow {
				cursorRow = promptStartRow
			}

			if cursorVCol > termW {
				cursorVCol = termW
			}
			if cursorVCol < 1 {
				cursorVCol = 1
			}

			if drawPrompt {
				fmt.Fprintf(&frameBuf, "\x1b[%d;%dH\x1b[?25h", cursorRow, cursorVCol)
			} else {
				promptCol := 1 + prefixLen
				fmt.Fprintf(&frameBuf, "\x1b[%d;%dH", promptStartRow, promptCol)
			}
		} else {
			prefixLen := utf8.RuneCountInString(stripAnsi(fullPrefixPlain))
			availWidth := termW - prefixLen - 1
			if availWidth < 10 {
				availWidth = 10
			}

			runes := []rune(inputLine)
			totalRunes := len(runes)

			start := 0
			displayStr := inputLine
			if totalRunes > availWidth {
				start = posOffset - (availWidth / 2)
				if start < 0 {
					start = 0
				}
				end := start + availWidth
				if end > totalRunes {
					end = totalRunes
					start = end - availWidth
					if start < 0 {
						start = 0
					}
				}
				displayStr = string(runes[start:end])
			}

			fmt.Fprintf(&frameBuf, "\x1b[%d;1H\x1b[2K", promptStartRow)
			frameBuf.WriteString(promptStr)
			if displayStr != "" {
				frameBuf.WriteString(displayStr)
			} else {
				getUI().StateMu.Lock()
				hint := getUI().PromptHint
				getUI().StateMu.Unlock()
				if hint != "" {
					hintRunes := []rune(hint)
					if len(hintRunes) > availWidth && availWidth > 5 {
						hint = string(hintRunes[:availWidth])
					}
					hintStyle := style.NewStyle().Foreground(activeTheme.Border).Italic(true)
					frameBuf.WriteString(hintStyle.Render(hint))
				}
			}

			cursorRow := promptStartRow
			cursorCol := 1 + prefixLen + (posOffset - start)
			if cursorCol > termW {
				cursorCol = termW
			}
			if cursorCol < 1+prefixLen {
				cursorCol = 1 + prefixLen
			}

			if drawPrompt {
				fmt.Fprintf(&frameBuf, "\x1b[%d;%dH\x1b[?25h", cursorRow, cursorCol)
			} else {
				prefixLen := utf8.RuneCountInString(stripAnsi(fullPrefixPlain))
				promptCol := 1 + prefixLen
				fmt.Fprintf(&frameBuf, "\x1b[%d;%dH", promptStartRow, promptCol)
			}
		}
	}

	_, _ = w.Write(frameBuf.Bytes())
}

func drawConsoleStaticControlsLocked(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal, drawPrompt bool) {
	DrawConsoleStaticControlsLocked(w, a, kiReader, rl, drawPrompt)
}

