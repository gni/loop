package session

import (
	"context"

	"loop/pkg/domain/message"
)

type MessageWriter interface {
	SaveMessageContext(context.Context, string, message.Message) error
}

type MessageLoader interface {
	LoadMessagesContext(context.Context, string) ([]message.Message, error)
}

type SessionRewriter interface {
	RewriteSessionContext(context.Context, string, []message.Message) error
}

type EntryAppender interface {
	AppendEntry(string, Record) error
}

type ContextEntryAppender interface {
	AppendEntryContext(context.Context, string, Record) error
}

type NameWriter interface {
	SetNameContext(context.Context, string, string) error
}

// ContextStore is the runtime storage contract. Workspace views share their
// underlying storage without mutating the scope of an existing session.
type ContextStore interface {
	Store
	MessageWriter
	MessageLoader
	SessionRewriter
	ContextEntryAppender
	NameWriter
	ScopedStore(string) ContextStore
	EntriesContext(context.Context, string) ([]Record, error)
	LatestForWorkspaceContext(context.Context, string) (string, error)
	UsageContext(context.Context, string) (message.Usage, error)
	ForkSessionContext(context.Context, string, string) (string, error)
	ClearSessionContext(context.Context, string) error
}
