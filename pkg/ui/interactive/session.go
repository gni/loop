package interactive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/term"

	"loop/pkg/db"
	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

func RunSessionExplorer(theme style.UITheme, rlInput io.Reader, rlOutput io.Writer) (string, bool, error) {
	ctx := context.Background()
	if provider, ok := rlInput.(interface{ GetInputContext() context.Context }); ok {
		if inputCtx := provider.GetInputContext(); inputCtx != nil {
			ctx = inputCtx
		}
	}
	return RunSessionExplorerContext(ctx, theme, rlInput, rlOutput)
}

func RunSessionExplorerContext(ctx context.Context, theme style.UITheme, rlInput io.Reader, rlOutput io.Writer) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	SetInInteractiveMenu(true)
	defer SetInInteractiveMenu(false)

	fd := int(os.Stdin.Fd())
	if f, ok := rlInput.(*os.File); ok {
		fd = int(f.Fd())
	} else if f, ok := rlOutput.(*os.File); ok {
		fd = int(f.Fd())
	}
	if term.IsTerminal(fd) {
		oldState, err := term.MakeRaw(fd)
		if err != nil {
			return "", false, fmt.Errorf("prepare session picker: %w", err)
		}
		defer term.Restore(fd, oldState)
	}

	sr := NewSessionReader(rlInput)
	defer sr.Close()
	sr.context, sr.cancelled = ctx, ctx.Done()

	if term.IsTerminal(fd) {
		terminal.Default().EnterAltScreen(rlOutput)
		defer terminal.Default().ExitAltScreen(rlOutput)
		signals := make(chan os.Signal, 1)
		terminal.NotifyResize(signals)
		defer signal.Stop(signals)
		return runSessionPickerContext(ctx, sr, signals, rlOutput, theme)
	}
	for {
		sessions, err := db.GetSessionsContext(ctx)
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
				if len([]rune(preview)) > 50 {
					preview = string([]rune(preview)[:50]) + "..."
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
			return "", false, ErrSelectionCancelled
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
			err = db.ClearSessionContext(ctx, sessionID)
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

func runSessionPicker(sr *SessionReader, signals chan os.Signal, rlOutput io.Writer, theme style.UITheme) (string, bool, error) {
	return runSessionPickerContext(context.Background(), sr, signals, rlOutput, theme)
}

func runSessionPickerContext(ctx context.Context, sr *SessionReader, signals chan os.Signal, rlOutput io.Writer, theme style.UITheme) (string, bool, error) {
	for {
		sessions, err := db.GetSessionsContext(ctx)
		if err != nil {
			return "", false, err
		}
		items := make([]string, 0, len(sessions)+2)
		for _, session := range sessions {
			label := session.Name
			if label == "" {
				label = session.SessionID
			}
			preview := strings.Join(strings.Fields(session.Preview), " ")
			date := session.Timestamp
			if len(date) > 16 {
				date = date[:16]
			}
			workspace := filepath.Base(session.Cwd)
			if session.Cwd == "" {
				workspace = "unknown workspace"
			}
			items = append(items, fmt.Sprintf("%s · %s · %s · %d messages · %s", label, date, workspace, session.MsgCount, preview))
		}
		labels := append([]string(nil), items...)
		items = append(items, "Start a new session")
		if len(sessions) > 0 {
			items = append(items, "Delete a session…")
		}
		index, err := RunInteractiveSelect(sr, signals, rlOutput, "loop sessions", items, theme)
		if err != nil {
			return "", false, err
		}
		if index < len(sessions) {
			return sessions[index].SessionID, false, nil
		}
		if index == len(sessions) {
			return "", true, nil
		}
		index, err = RunInteractiveSelect(sr, signals, rlOutput, "delete session · esc to return", labels, theme)
		if err != nil {
			if !errors.Is(err, ErrSelectionCancelled) {
				return "", false, err
			}
			continue
		}
		confirmation, err := RunInteractiveSelect(sr, signals, rlOutput, "Delete "+sessions[index].SessionID+"?", []string{"Cancel", "Delete"}, theme)
		if err != nil && !errors.Is(err, ErrSelectionCancelled) {
			return "", false, err
		}
		if err != nil || confirmation != 1 {
			continue
		}
		if err := db.ClearSessionContext(ctx, sessions[index].SessionID); err != nil {
			return "", false, err
		}
	}
}
