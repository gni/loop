package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
)

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage persistent conversation sessions",
}

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all saved conversation sessions and metadata",
	Run: func(cmd *cobra.Command, args []string) {
		if err := initSessionStore(); err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		sessions, err := db.GetSessions()
		if err != nil || len(sessions) == 0 {
			fmt.Println("No past sessions found.")
			return
		}

		fmt.Println("Past Conversation Sessions:")
		for _, s := range sessions {
			preview := s.Preview
			if len(preview) > 50 {
				preview = preview[:50] + "..."
			}
			ts := s.Timestamp
			if len(ts) > 16 {
				ts = ts[:16]
			}
			fmt.Printf("  - %s [%s] (%d messages) - %s\n", s.SessionID, ts, s.MsgCount, preview)
		}
	},
}

var (
	forkFrom  string
	forkEntry string
)

var sessionNewCmd = &cobra.Command{
	Use:   "new",
	Short: "Start a new session and output its UUID",
	Run: func(cmd *cobra.Command, args []string) {
		if err := initSessionStore(); err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		if forkFrom != "" {
			newID, err := db.ForkSession(forkFrom, forkEntry)
			if err != nil {
				fmt.Printf("Error: %v\n", err)
				return
			}
			if err := db.SetLatestSessionID(newID); err != nil {
				fmt.Printf("Error: %v\n", err)
				return
			}
			fmt.Println(newID)
			return
		}

		newID := db.NewUUID()
		cfg, _ := config.LoadConfig(configPath)
		a := agent.NewAgent(cfg, configPath, nil)
		sysMsg := db.Message{Role: "system", Content: a.GetSystemPrompt()}
		_ = db.ClearSession(newID)
		_ = db.SaveMessage(newID, sysMsg)

		fmt.Println(newID)
	},
}

var sessionClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear all saved conversation sessions from disk",
	Run: func(cmd *cobra.Command, args []string) {
		if err := initSessionStore(); err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		if err := db.ClearHistory(); err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		fmt.Println("All saved conversation sessions cleared from disk.")
	},
}

func init() {
	sessionNewCmd.Flags().StringVar(&forkFrom, "fork", "", "Fork from an existing session ID (creates a new DAG session; parent file is untouched)")
	sessionNewCmd.Flags().StringVar(&forkEntry, "fork-entry", "", "Entry ID to fork up to within the parent session (defaults to its leaf)")
	sessionCmd.AddCommand(sessionListCmd, sessionNewCmd, sessionClearCmd)
	rootCmd.AddCommand(sessionCmd)
}