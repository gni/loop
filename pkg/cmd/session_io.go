package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"loop/pkg/db"
	"loop/pkg/domain/session"
	"loop/pkg/storage"
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
		return store.SetNameContext(cmd.Context(), args[0], args[1])
	}})
	sessionCmd.AddCommand(&cobra.Command{Use: "delete <session>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		store, err := sessionStore()
		if err != nil {
			return err
		}
		return store.ClearSessionContext(cmd.Context(), args[0])
	}})
	sessionCmd.AddCommand(&cobra.Command{Use: "search <query>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		store, err := sessionStore()
		if err != nil {
			return err
		}
		entries, err := store.GetSessionsContext(cmd.Context())
		if err != nil {
			return err
		}
		query := strings.ToLower(args[0])
		found := make([]session.SessionInfo, 0)
		for _, entry := range entries {
			matches := strings.Contains(strings.ToLower(entry.Name+" "+entry.SessionID+" "+entry.Preview), query)
			if !matches {
				records, err := store.EntriesContext(cmd.Context(), entry.SessionID)
				if err != nil {
					return err
				}
				for _, record := range records {
					if strings.Contains(strings.ToLower(record.Message.Content), query) {
						matches = true
						break
					}
					for _, message := range record.Messages {
						if strings.Contains(strings.ToLower(message.Content), query) {
							matches = true
							break
						}
					}
					if matches {
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
		return store.ExportContext(cmd.Context(), args[0], cmd.OutOrStdout())
	}})
	sessionCmd.AddCommand(&cobra.Command{Use: "import [file]", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		store, err := sessionStore()
		if err != nil {
			return err
		}
		input := cmd.InOrStdin()
		if len(args) > 0 {
			file, err := storage.OpenRegularFile(args[0])
			if err != nil {
				return err
			}
			defer file.Close()
			input = file
		}
		input, stopInput := cancellableInput(cmd.Context(), input)
		defer stopInput()
		id, err := store.ImportContext(cmd.Context(), input)
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
		entries, err := store.EntriesContext(cmd.Context(), args[0])
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
		return store.BranchContext(cmd.Context(), args[0], args[1])
	}})
	sessionCmd.AddCommand(&cobra.Command{Use: "repair-tail <session>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		store, err := sessionStore()
		if err != nil {
			return err
		}
		return store.RepairTailContext(cmd.Context(), args[0])
	}})
}
