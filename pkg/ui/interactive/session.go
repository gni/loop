package interactive

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func RunSessionExplorer(theme style.UITheme, rlInput io.Reader, rlOutput io.Writer) (string, bool, error) {
	SetInInteractiveMenu(true)
	defer SetInInteractiveMenu(false)

	fd := int(os.Stdin.Fd())
	if f, ok := rlInput.(*os.File); ok {
		fd = int(f.Fd())
	} else if f, ok := rlOutput.(*os.File); ok {
		fd = int(f.Fd())
	}
	if term.IsTerminal(fd) {
		if oldState, err := term.MakeRaw(fd); err == nil {
			defer term.Restore(fd, oldState)
		}
	}

	sr := NewSessionReader(rlInput)
	defer sr.Close()

	for {
		sessions, err := db.GetSessions()
		if err != nil {
			return "", false, err
		}

		fmt.Fprint(rlOutput, "\r\n==================================================\r\n")
		fmt.Fprint(rlOutput, "                 loop sessions                   \r\n")
		fmt.Fprint(rlOutput, "==================================================\r\n")
		if len(sessions) == 0 {
			fmt.Fprint(rlOutput, "no past sessions found.\r\n")
		} else {
			for i, s := range sessions {
				preview := s.Preview
				if len(preview) > 50 {
					preview = preview[:50] + "..."
				}
				timestamp := s.Timestamp
				if len(timestamp) > 16 {
					timestamp = timestamp[:16]
				}
				fmt.Fprintf(rlOutput, "  [%d] %s (%d messages) - %s\r\n", i+1, timestamp, s.MsgCount, preview)
			}
		}
		fmt.Fprint(rlOutput, "--------------------------------------------------\r\n")
		fmt.Fprint(rlOutput, "options:\r\n")
		fmt.Fprint(rlOutput, "  <number>     : load session\r\n")
		fmt.Fprint(rlOutput, "  d <number>   : delete session\r\n")
		fmt.Fprint(rlOutput, "  n            : start a new session\r\n")
		fmt.Fprint(rlOutput, "  q            : cancel and return\r\n")
		fmt.Fprint(rlOutput, "\r\nchoose option: ")

		inputStr, err := ReadInputRaw(sr, rlOutput)
		if err != nil {
			return "", false, err
		}
		inputStr = strings.TrimSpace(inputStr)

		if inputStr == "" {
			continue
		}
		if inputStr == "q" || inputStr == "exit" {
			return "", false, fmt.Errorf("cancelled")
		}
		if inputStr == "n" {
			return "", true, nil
		}

		if strings.HasPrefix(inputStr, "d ") {
			numStr := strings.TrimSpace(strings.TrimPrefix(inputStr, "d "))
			num, err := strconv.Atoi(numStr)
			if err != nil || num < 1 || num > len(sessions) {
				fmt.Fprint(rlOutput, "invalid session number to delete.\r\n")
				continue
			}
			sessionID := sessions[num-1].SessionID
			err = db.ClearSession(sessionID)
			if err != nil {
				fmt.Fprintf(rlOutput, "failed to delete session: %v\r\n", err)
			} else {
				fmt.Fprintf(rlOutput, "deleted session %s successfully.\r\n", sessionID)
			}
			continue
		}

		num, err := strconv.Atoi(inputStr)
		if err == nil && num >= 1 && num <= len(sessions) {
			return sessions[num-1].SessionID, false, nil
		}

		fmt.Fprint(rlOutput, "invalid choice. please try again.\r\n")
	}
}
