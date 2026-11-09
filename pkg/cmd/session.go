package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"loop/pkg/db"
	"loop/pkg/ui/style"
)

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage persistent conversation sessions",
}

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all saved conversation sessions and metadata",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := initSessionStore(); err != nil {
			return err
		}

		sessions, err := db.GetSessionsContext(cmd.Context())
		if err != nil {
			return fmt.Errorf("list sessions: %w", err)
		}
		if len(sessions) == 0 {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "No past sessions found.")
			return err
		}

		if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Past Conversation Sessions:"); err != nil {
			return err
		}
		for _, s := range sessions {
			preview := s.Preview
			preview = style.TruncateCells(preview, 50)
			ts := s.Timestamp
			if len(ts) > 16 {
				ts = ts[:16]
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  - %s [%s] (%d messages) - %s\n", s.SessionID, ts, s.MsgCount, preview); err != nil {
				return err
			}
		}
		return nil
	},
}

var (
	forkFrom  string
	forkEntry string
)

var sessionNewCmd = &cobra.Command{
	Use:   "new",
	Short: "Start a new session and output its UUID",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := initSessionStore(); err != nil {
			return err
		}

		if forkFrom != "" {
			newID, err := db.ForkSessionContext(cmd.Context(), forkFrom, forkEntry)
			if err != nil {
				return err
			}
			if err := db.SetLatestSessionIDContext(cmd.Context(), newID); err != nil {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), 2*time.Second)
				defer cancel()
				return errors.Join(err, db.ClearSessionContext(cleanupCtx, newID))
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), newID)
			return err
		}

		newID := db.NewUUID()
		_, err := fmt.Fprintln(cmd.OutOrStdout(), newID)
		return err
	},
}

var sessionClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear all saved conversation sessions from disk",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := initSessionStore(); err != nil {
			return err
		}

		if err := db.ClearHistoryContext(cmd.Context()); err != nil {
			return err
		}

		_, err := fmt.Fprintln(cmd.OutOrStdout(), "All saved conversation sessions cleared from disk.")
		return err
	},
}

func init() {
	sessionNewCmd.Flags().StringVar(&forkFrom, "fork", "", "Fork from an existing session ID (creates a new DAG session; parent file is untouched)")
	sessionNewCmd.Flags().StringVar(&forkEntry, "fork-entry", "", "Entry ID to fork up to within the parent session (defaults to its leaf)")
	sessionCmd.AddCommand(sessionListCmd, sessionNewCmd, sessionClearCmd)
	rootCmd.AddCommand(sessionCmd)
}
