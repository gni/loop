package db

import (
	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
)

func (s *JSONLStore) Usage(id string) (message.Usage, error) {
	entries, err := s.Entries(id)
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
