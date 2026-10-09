package interactive

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func RunSessionExplorer(theme style.UITheme, rlInput io.Reader, rlOutput io.Writer) (string, bool, error) {
	for {
		sessions, err := db.GetSessions()
		if err != nil {
			return "", false, err
		}

		fmt.Fprintln(rlOutput, "\n==================================================")
		fmt.Fprintln(rlOutput, "                 loop sessions                   ")
		fmt.Fprintln(rlOutput, "==================================================")
		if len(sessions) == 0 {
			fmt.Fprintln(rlOutput, "no past sessions found.")
		} else {
			for i, s := range sessions {
				preview := s.Preview
				if len(preview) > 50 {
					preview = preview[:50] + "..."
				}
				fmt.Fprintf(rlOutput, "  [%d] %s (%d messages) - %s\n", i+1, s.Timestamp[:16], s.MsgCount, preview)
			}
		}
		fmt.Fprintln(rlOutput, "--------------------------------------------------")
		fmt.Fprintln(rlOutput, "options:")
		fmt.Fprintln(rlOutput, "  <number>     : load session")
		fmt.Fprintln(rlOutput, "  d <number>   : delete session")
		fmt.Fprintln(rlOutput, "  n            : start a new session")
		fmt.Fprintln(rlOutput, "  q            : cancel and return")
		fmt.Fprint(rlOutput, "\nchoose option: ")

		inputStr, err := ReadInputRaw(rlInput, rlOutput)
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
				fmt.Fprintln(rlOutput, "invalid session number to delete.")
				continue
			}
			sessionID := sessions[num-1].SessionID
			err = db.ClearSession(sessionID)
			if err != nil {
				fmt.Fprintf(rlOutput, "failed to delete session: %v\n", err)
			} else {
				fmt.Fprintf(rlOutput, "deleted session %s successfully.\n", sessionID)
			}
			continue
		}

		num, err := strconv.Atoi(inputStr)
		if err == nil && num >= 1 && num <= len(sessions) {
			return sessions[num-1].SessionID, false, nil
		}

		fmt.Fprintln(rlOutput, "invalid choice. please try again.")
	}
}
