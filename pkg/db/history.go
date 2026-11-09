package db

import (
	"context"
	"os"
	"sort"
)

type sessionHistoryItem struct {
	startTime string
	prompts   []string
}

func (s *JSONLStore) GetUserHistory() ([]string, error) {
	return s.GetUserHistoryContext(context.Background())
}

// GetUserHistoryContext allows cancellation while waiting for session storage.
func (s *JSONLStore) GetUserHistoryContext(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.checkReady(""); err != nil {
		return nil, err
	}

	release, err := s.lockStore(ctx, false)
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sessionID, ok := validSessionEntry(entry)
		if !ok {
			continue
		}
		sum, ok := s.summarizeSessionLockedContext(ctx, sessionID, entry)
		if !ok || len(sum.prompts) == 0 {
			continue
		}
		items = append(items, sessionHistoryItem{
			startTime: sum.startTime,
			prompts:   sum.prompts,
		})
	}

	if err := ctx.Err(); err != nil {
		return nil, err
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

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return history, nil
}

func GetUserHistory() ([]string, error) {
	return GetUserHistoryContext(context.Background())
}

func GetUserHistoryContext(ctx context.Context) ([]string, error) {
	return withStoreVal(func(s *JSONLStore) ([]string, error) { return s.GetUserHistoryContext(ctx) })
}
