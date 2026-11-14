package db

import (
	"context"
	"os"

	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
)

// CheckpointSessionContext appends only new messages when saved context remains
// an exact prefix. Genuine replacements still use the existing context record.
func (s *JSONLStore) CheckpointSessionContext(ctx context.Context, id string, messages []message.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkReady(id); err != nil {
		return err
	}
	release, err := s.lockStore(ctx, true)
	if err != nil {
		return err
	}
	defer release()
	entries, err := s.entriesContext(ctx, id)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	// Compare the same projection that loading exposes. Ignored legacy slash
	// commands and empty inputs must not cause a rewrite on every later turn.
	retained := make([]message.Message, 0, len(messages))
	for _, msg := range messages {
		if !isSkippedSlashRecord(msg) {
			retained = append(retained, msg)
		}
	}
	saved := ProjectMessages(entries)
	appendOnly := len(saved) <= len(retained)
	if appendOnly {
		for index, msg := range saved {
			if err := ctx.Err(); err != nil {
				return err
			}
			equal, err := message.EqualPersisted(msg, retained[index])
			if err != nil {
				return err
			}
			if !equal {
				appendOnly = false
				break
			}
		}
	}
	if !appendOnly {
		return s.appendEntryLocked(ctx, id, session.Record{Type: "context", Messages: retained})
	}
	if len(saved) == len(retained) {
		return ctx.Err()
	}
	records := make([]session.Record, len(retained)-len(saved))
	for index, msg := range retained[len(saved):] {
		records[index] = session.Record{Type: "message", Message: msg}
	}
	cached, err := s.appendStateContext(ctx, id)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return s.commitSessionRecordsLocked(ctx, id, records, false, cached, os.IsNotExist(err), nil)
}
