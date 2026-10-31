package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func init() {
	var keyName string
	command := &cobra.Command{
		Use:   "keycheck",
		Short: "Compare Enter with a modified Enter key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			names := map[string]string{"shift-enter": "Shift+Enter", "ctrl-enter": "Ctrl+Enter", "alt-enter": "Alt+Enter"}
			label, ok := names[keyName]
			if !ok {
				return fmt.Errorf("--key must be shift-enter, ctrl-enter, or alt-enter")
			}
			sequence := map[string]string{"shift-enter": "ESC[13;2u", "ctrl-enter": "ESC[13;5u", "alt-enter": "ESC[13;3u"}[keyName]

			fd := int(os.Stdin.Fd())
			if !term.IsTerminal(fd) {
				return fmt.Errorf("keycheck requires an interactive terminal")
			}
			previous, err := term.MakeRaw(fd)
			if err != nil {
				return err
			}
			defer term.Restore(fd, previous)
			w := cmd.OutOrStdout()
			// Use the same enhanced keyboard reporting as the REPL.
			fmt.Fprint(w, "\x1b[>4;2m\x1b[>1u")
			defer fmt.Fprint(w, "\x1b[<u\x1b[>4m")
			fmt.Fprint(w, "Press Enter (Ctrl+C cancels): ")
			enter, err := readTerminalKey(cmd.Context(), os.Stdin)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "\r\nEnter:       %q  [% x]\r\n", enter, enter)
			fmt.Fprintf(w, "Now press %s: ", label)
			modifiedEnter, err := readTerminalKey(cmd.Context(), os.Stdin)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "\r\n%s: %q  [% x]\r\n", label, modifiedEnter, modifiedEnter)
			if bytes.Equal(enter, modifiedEnter) {
				fmt.Fprintf(w, "Identical input: Loop cannot distinguish these keys in this terminal.\r\nConfigure the terminal's %s binding to send %s.\r\nEnter remains Send. Ctrl+N inserts a newline.\r\n", label, sequence)
			} else {
				fmt.Fprint(w, "Distinct input received. Include these two key lines when reporting a keyboard issue.\r\n")
			}
			return nil
		},
	}
	command.Flags().StringVar(&keyName, "key", "shift-enter", "Key to compare: shift-enter, ctrl-enter, alt-enter")
	rootCmd.AddCommand(command)
}

func readTerminalKey(ctx context.Context, input *os.File) ([]byte, error) {
	var key []byte
	for len(key) < 64 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fds := []unix.PollFd{{Fd: int32(input.Fd()), Events: unix.POLLIN}}
		ready, err := unix.Poll(fds, 100)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, err
		}
		if ready == 0 {
			if len(key) > 0 {
				return key, nil
			}
			continue
		}
		var b [1]byte
		n, err := input.Read(b[:])
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, io.EOF
		}
		key = append(key, b[0])
		if len(key) == 1 {
			if b[0] == 3 || b[0] == 4 {
				return nil, context.Canceled
			}
			if b[0] != 27 {
				return key, nil
			}
		} else if len(key) == 2 {
			if b[0] != '[' && b[0] != 'O' {
				return key, nil
			}
		} else if b[0] >= 0x40 && b[0] <= 0x7e {
			if string(key) == "\x1b[99;5u" || string(key) == "\x1b[100;5u" {
				return nil, context.Canceled
			}
			return key, nil
		}
	}
	return key, nil
}
