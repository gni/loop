package cmd

import (
	"fmt"
	"path/filepath"

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
		sessionsDir := filepath.Join(filepath.Dir(configPath), "sessions")
		if err := db.InitDB(sessionsDir); err != nil {
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
			fmt.Printf("  - %s [%s] (%d messages) - %s\n", s.SessionID, s.Timestamp[:16], s.MsgCount, preview)
		}
	},
}

var sessionNewCmd = &cobra.Command{
	Use:   "new",
	Short: "Start a new session and output its UUID",
	Run: func(cmd *cobra.Command, args []string) {
		sessionsDir := filepath.Join(filepath.Dir(configPath), "sessions")
		if err := db.InitDB(sessionsDir); err != nil {
			fmt.Printf("Error: %v\n", err)
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
		sessionsDir := filepath.Join(filepath.Dir(configPath), "sessions")
		if err := db.InitDB(sessionsDir); err != nil {
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
	sessionCmd.AddCommand(sessionListCmd, sessionNewCmd, sessionClearCmd)
	rootCmd.AddCommand(sessionCmd)
}
