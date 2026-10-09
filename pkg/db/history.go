package db

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
)

type sessionWithStart struct {
	filePath  string
	startTime string
}

func (s *JSONLStore) GetUserHistory() ([]string, error) {
	if s == nil || s.dir == "" {
		return nil, fmt.Errorf("sessions directory not initialized")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var files []sessionWithStart

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}

		sessionID := strings.TrimSuffix(entry.Name(), ".jsonl")
		if err := session.ValidateID(sessionID); err != nil {
			continue
		}

		filePath := filepath.Join(s.dir, entry.Name())
		startTime := ""

		f, err := os.Open(filePath)
		if err == nil {
			scanner := bufio.NewScanner(f)
			if scanner.Scan() {
				lineBytes := scanner.Bytes()
				var record session.Record
				if json.Unmarshal(lineBytes, &record) == nil {
					startTime = record.Timestamp
				}
			}
			f.Close()
		}

		if startTime == "" {
			startTime = entryModTimeOrNow(entry)
		}

		files = append(files, sessionWithStart{
			filePath:  filePath,
			startTime: startTime,
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].startTime < files[j].startTime
	})

	var history []string
	var lastContent string

	for _, item := range files {
		f, err := os.Open(item.filePath)
		if err != nil {
			continue
		}

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			lineBytes := scanner.Bytes()
			var record session.Record
			if err := json.Unmarshal(lineBytes, &record); err == nil {
				msg := record.Message
				if msg.Role == message.RoleUser && !strings.HasPrefix(strings.ToLower(msg.Content), "[user manually executed") {
					content := strings.TrimSpace(msg.Content)
					if content != "" && content != lastContent {
						history = append(history, content)
						lastContent = content
					}
				}
			}
		}
		f.Close()
	}

	return history, nil
}

func GetUserHistory() ([]string, error) {
	store, err := getStore()
	if err != nil {
		return nil, err
	}
	return store.GetUserHistory()
}
