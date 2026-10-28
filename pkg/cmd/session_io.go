package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"loop/pkg/db"
	"loop/pkg/domain/session"
	"os"
	"strings"
)

func sessionStore() (*db.JSONLStore, error) {
	if err := initSessionStore(); err != nil {
		return nil, err
	}
	return db.DefaultStore()
}

func init() {
	sessionCmd.AddCommand(&cobra.Command{Use: "name <session> <name>", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		store, err := sessionStore()
		if err != nil {
			return err
		}
		return store.SetName(args[0], args[1])
	}})
	sessionCmd.AddCommand(&cobra.Command{Use: "delete <session>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		store, err := sessionStore()
		if err != nil {
			return err
		}
		return store.ClearSession(args[0])
	}})
	sessionCmd.AddCommand(&cobra.Command{Use: "search <query>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		store, err := sessionStore()
		if err != nil {
			return err
		}
		entries, err := store.GetSessions()
		if err != nil {
			return err
		}
		var found []session.SessionInfo
		for _, entry := range entries {
			matches := strings.Contains(strings.ToLower(entry.Name+" "+entry.SessionID+" "+entry.Preview), strings.ToLower(args[0]))
			if !matches {
				records, err := store.Entries(entry.SessionID)
				if err != nil {
					return err
				}
				for _, record := range records {
					if strings.Contains(strings.ToLower(record.Message.Content), strings.ToLower(args[0])) {
						matches = true
						break
					}
				}
			}
			if matches {
				found = append(found, entry)
			}
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(found)
	}})
	sessionCmd.AddCommand(&cobra.Command{Use: "export <session>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		store, err := sessionStore()
		if err != nil {
			return err
		}
		return store.Export(args[0], cmd.OutOrStdout())
	}})
	sessionCmd.AddCommand(&cobra.Command{Use: "import [file]", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		store, err := sessionStore()
		if err != nil {
			return err
		}
		input := cmd.InOrStdin()
		if len(args) > 0 {
			file, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer file.Close()
			input = file
		}
		id, err := store.Import(input)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), id)
		return err
	}})
	sessionCmd.AddCommand(&cobra.Command{Use: "tree <session>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		store, err := sessionStore()
		if err != nil {
			return err
		}
		entries, err := store.Entries(args[0])
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(entries)
	}})
	sessionCmd.AddCommand(&cobra.Command{Use: "branch <session> <entry>", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		store, err := sessionStore()
		if err != nil {
			return err
		}
		return store.Branch(args[0], args[1])
	}})
	sessionCmd.AddCommand(&cobra.Command{Use: "repair-tail <session>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		store, err := sessionStore()
		if err != nil {
			return err
		}
		return store.RepairTail(args[0])
	}})
}
