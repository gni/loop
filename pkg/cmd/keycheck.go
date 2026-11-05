package cmd

import (
	"bytes"
	"fmt"
	"os"

	"github.com/spf13/cobra"
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
