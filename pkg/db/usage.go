package db

import (
	"context"

	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
)

func (s *JSONLStore) Usage(id string) (message.Usage, error) {
	return s.UsageContext(context.Background(), id)
}

// UsageContext allows cancellation while waiting for session storage.
func (s *JSONLStore) UsageContext(ctx context.Context, id string) (message.Usage, error) {
	if err := ctx.Err(); err != nil {
		return message.Usage{}, err
	}
	entries, err := s.EntriesContext(ctx, id)
	if err != nil {
		return message.Usage{}, err
	}
	var messages []message.Message
	for _, entry := range session.LeafPath(entries, "") {
		switch entry.Type {
		case "message", "", "compaction":
			messages = append(messages, entry.Message)
		}
	}
	return message.TotalUsage(messages), nil
}
