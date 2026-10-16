package db

import (
	"bufio"
	"encoding/json"
	"os"
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
	if err := s.checkReady(""); err != nil {
		return nil, err
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
		sessionID, ok := validSessionEntry(entry)
		if !ok {
			continue
		}

		filePath := s.sessionPath(sessionID)
		startTime := ""

		if f, err := os.Open(filePath); err == nil {
			scanner := bufio.NewScanner(f)
			if scanner.Scan() {
				var record session.Record
				if json.Unmarshal(scanner.Bytes(), &record) == nil {
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
			var record session.Record
			if err := json.Unmarshal(scanner.Bytes(), &record); err == nil {
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