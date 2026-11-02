package db

import (
	"os"
	"sort"
)

type sessionHistoryItem struct {
	startTime string
	prompts   []string
}

func (s *JSONLStore) GetUserHistory() ([]string, error) {
	if err := s.checkReady(""); err != nil {
		return nil, err
	}

	s.core.mu.Lock()
	defer s.core.mu.Unlock()
	release, err := s.lock(false)
	if err != nil {
		return nil, err
	}
	defer release()

	entries, err := os.ReadDir(s.core.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	items := make([]sessionHistoryItem, 0, len(entries))
	for _, entry := range entries {
		sessionID, ok := validSessionEntry(entry)
		if !ok {
			continue
		}
		sum, ok := s.summarizeSessionLocked(sessionID, entry)
		if !ok || len(sum.prompts) == 0 {
			continue
		}
		items = append(items, sessionHistoryItem{
			startTime: sum.startTime,
			prompts:   sum.prompts,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].startTime < items[j].startTime
	})

	var history []string
	var lastContent string
	for _, item := range items {
		for _, content := range item.prompts {
			if content != "" && content != lastContent {
				history = append(history, content)
				lastContent = content
			}
		}
	}

	return history, nil
}

func GetUserHistory() ([]string, error) {
	return withStoreVal(func(s *JSONLStore) ([]string, error) { return s.GetUserHistory() })
}
