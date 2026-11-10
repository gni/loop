package db

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
)

const maxJSONLLineBytes = 64 * 1024 * 1024

type ToolFunction = message.ToolFunction
type ToolCall = message.ToolCall
type Message = message.Message
type SessionInfo = session.SessionInfo

func NewUUID() string {
	return session.NewUUID()
}

type cachedSummary struct {
	size       int64
	modified   time.Time
	hasVisible bool
	info       session.SessionInfo
	startTime  string
	prompts    []string
}

type storeCore struct {
	dir          string
	mu           sync.Mutex
	cache        map[string]cachedEntries
	summaryCache map[string]cachedSummary
	cacheClock   uint64
}

var (
	coresMu    sync.Mutex
	coresByDir = make(map[string]*storeCore)
)

func getOrCreateCore(cleanDir string) *storeCore {
	coresMu.Lock()
	defer coresMu.Unlock()
	if existing, ok := coresByDir[cleanDir]; ok {
		return existing
	}
	core := &storeCore{
		dir:          cleanDir,
		cache:        make(map[string]cachedEntries),
		summaryCache: make(map[string]cachedSummary),
	}
	coresByDir[cleanDir] = core
	return core
}

type JSONLStore struct {
	core      *storeCore
	Workspace string
}

var _ session.Store = (*JSONLStore)(nil)
var _ session.ContextStore = (*JSONLStore)(nil)

func NewJSONLStore(dirPath string) (*JSONLStore, error) {
	clean := filepath.Clean(dirPath)
	if abs, err := filepath.Abs(clean); err == nil {
		clean = abs
	}
	if err := os.MkdirAll(clean, 0700); err != nil {
		return nil, fmt.Errorf("failed to create sessions directory: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	return &JSONLStore{core: getOrCreateCore(clean), Workspace: cwd}, nil
}

func newJSONLScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxJSONLLineBytes)
	return scanner
}

func isSkippedSlashRecord(msg message.Message) bool {
	if msg.Role != message.RoleUser {
		return false
	}
	return (strings.TrimSpace(msg.Content) == "" && len(msg.ContentBlocks) == 0) || strings.HasPrefix(msg.Content, "[user manually executed slash command:")
}

func (s *JSONLStore) sessionPath(sessionID string) string {
	return filepath.Join(s.core.dir, sessionID+".jsonl")
}

func (s *JSONLStore) latestSessionPath() string {
	return filepath.Join(s.core.dir, ".latest_session")
}

func nowTimestamp() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

func validSessionEntry(entry os.DirEntry) (string, bool) {
	if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
		return "", false
	}
	sessionID := strings.TrimSuffix(entry.Name(), ".jsonl")
	if session.ValidateID(sessionID) != nil {
		return "", false
	}
	return sessionID, true
}

func writeJSONLine(w io.Writer, v any) error {
	jsonData, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(jsonData)+1 >= maxJSONLLineBytes {
		return fmt.Errorf("session record exceeds %d MiB", maxJSONLLineBytes>>20)
	}
	data := append(jsonData, '\n')
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}

func (s *JSONLStore) checkReady(sessionID string) error {
	if sessionID != "" {
		if err := session.ValidateID(sessionID); err != nil {
			return err
		}
	}
	if s == nil || s.core == nil || s.core.dir == "" {
		return fmt.Errorf("sessions directory not initialized")
	}
	return nil
}

func (s *JSONLStore) SaveMessage(sessionID string, msg message.Message) error {
	return s.SaveMessageContext(context.Background(), sessionID, msg)
}

// SaveMessageContext allows cancellation while waiting for session storage.
func (s *JSONLStore) SaveMessageContext(ctx context.Context, sessionID string, msg message.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if isSkippedSlashRecord(msg) {
		return nil
	}
	return s.AppendEntryContext(ctx, sessionID, session.Record{Type: "message", Message: msg})
}

func (s *JSONLStore) LoadMessages(sessionID string) ([]message.Message, error) {
	return s.LoadMessagesContext(context.Background(), sessionID)
}

// LoadMessagesContext allows cancellation while waiting for session storage.
func (s *JSONLStore) LoadMessagesContext(ctx context.Context, sessionID string) ([]message.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.checkReady(sessionID); err != nil {
		return nil, err
	}

	entries, err := s.EntriesContext(ctx, sessionID)

	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("load session %s: %w", sessionID, err)
	}

	return ProjectMessages(entries), nil
}

// ProjectMessages restores model context from an already loaded record snapshot.
func ProjectMessages(entries []session.Record) []message.Message {
	projected := session.Project(entries)
	messages := make([]message.Message, 0, len(projected))
	for _, msg := range projected {
		if !isSkippedSlashRecord(msg) {
			messages = append(messages, msg)
		}
	}
	return messages
}

func (s *JSONLStore) HasMessages(sessionID string) bool {
	if s.checkReady(sessionID) != nil {
		return false
	}
	filePath := s.sessionPath(sessionID)
	info, err := os.Stat(filePath)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func (s *JSONLStore) RewriteSession(sessionID string, messages []message.Message) error {
	return s.RewriteSessionContext(context.Background(), sessionID, messages)
}

// RewriteSessionContext allows cancellation while waiting for session storage.
func (s *JSONLStore) RewriteSessionContext(ctx context.Context, sessionID string, messages []message.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.AppendEntryContext(ctx, sessionID, session.Record{Type: "context", Messages: messages})
}

func (s *JSONLStore) AppendEntry(sessionID string, record session.Record) (err error) {
	return s.AppendEntryContext(context.Background(), sessionID, record)
}

// AppendEntryContext allows cancellation while waiting for session storage.
func (s *JSONLStore) AppendEntryContext(ctx context.Context, sessionID string, record session.Record) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkReady(sessionID); err != nil {
		return err
	}
	release, err := s.lockStore(ctx, true)
	if err != nil {
		return err
	}
	defer release()
	return s.appendEntryLocked(ctx, sessionID, record)
}

// The caller owns both the store file lock and cache mutex through commit.
func (s *JSONLStore) appendEntryLocked(ctx context.Context, sessionID string, record session.Record) (err error) {
	return s.appendRecordLocked(ctx, sessionID, record, false)
}

// Branch markers supply their parent explicitly; ordinary records follow the
// current leaf. Both use the same durable append and cache invalidation path.
func (s *JSONLStore) appendRecordLocked(ctx context.Context, sessionID string, record session.Record, explicitParent bool) (err error) {
	cached, err := s.appendStateContext(ctx, sessionID)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	created := os.IsNotExist(err)
	if err := ctx.Err(); err != nil {
		return err
	}
	// Settings are snapshots. Repeating the active branch's current value
	// must not grow history on every turn, sync, and shutdown.
	var settingsHash *[sha256.Size]byte
	if record.Type == "settings" {
		hash := sha256.Sum256(record.Data)
		if cached.settingsHash != nil && *cached.settingsHash == hash {
			return nil
		}
		settingsHash = &hash
	}
	if !explicitParent {
		record.ParentID = ""
		if cached.count > 0 && cached.lastType != "session" {
			record.ParentID = cached.lastID
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// An interrupted append must never leave a reusable stale cache entry.
	s.invalidateSessionCacheLocked(sessionID)
	record.ID = session.NewUUID()
	record.Timestamp = nowTimestamp()
	f, err := os.OpenFile(s.sessionPath(sessionID), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	infoBefore, err := f.Stat()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if err != nil && !committed {
			err = rollbackSessionAppend(f, infoBefore.Size(), created, err)
		}
	}()
	if err := ensureSessionAppendBoundary(f, infoBefore.Size()); err != nil {
		return err
	}

	if cached.count == 0 {
		header := session.Record{Type: "session", Version: session.CurrentSessionVersion, ID: sessionID, Cwd: s.Workspace, Timestamp: nowTimestamp()}
		if err := writeJSONLine(f, header); err != nil {
			return err
		}
		cached.count++
		if cached.complete {
			cached.entries = append(cached.entries, header)
		}
	}
	if err := writeJSONLine(f, record); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		delete(s.core.cache, sessionID)
		return err
	}
	if err := s.setLatestSessionIDUnlocked(sessionID); err != nil {
		return err
	}
	committed = true
	info, err := f.Stat()
	if err != nil {
		return err
	}
	cached.size, cached.modified = info.Size(), info.ModTime()
	cached.count++
	cached.lastID, cached.lastType = record.ID, record.Type
	if settingsHash != nil {
		cached.settingsHash = settingsHash
	}
	if cached.complete {
		cached.entries = append(cached.entries, cloneRecord(record))
	}
	// A branch changes which settings snapshot is active. Rebuild its parent
	// ancestry before deduplicating a later settings append.
	if record.Type != "branch" {
		s.cacheEntries(sessionID, cached)
	}
	return nil
}

func AppendEntry(sessionID string, record session.Record) error {
	return AppendEntryContext(context.Background(), sessionID, record)
}

func AppendEntryContext(ctx context.Context, sessionID string, record session.Record) error {
	return withStore(func(s *JSONLStore) error { return s.AppendEntryContext(ctx, sessionID, record) })
}

var (
	defaultStore *JSONLStore
	storeMu      sync.RWMutex
)

func SetDefaultStore(store *JSONLStore) {
	if store == nil {
		return
	}
	storeMu.Lock()
	defaultStore = store
	storeMu.Unlock()
}

func InitDB(dirPath string) error {
	store, err := NewJSONLStore(dirPath)
	if err != nil {
		return err
	}
	SetDefaultStore(store)
	return nil
}

func getStore() (*JSONLStore, error) {
	storeMu.RLock()
	defer storeMu.RUnlock()
	if defaultStore == nil {
		return nil, fmt.Errorf("sessions directory not initialized")
	}
	return defaultStore, nil
}

func withStore(fn func(*JSONLStore) error) error {
	store, err := getStore()
	if err != nil {
		return err
	}
	return fn(store)
}

func withStoreVal[T any](fn func(*JSONLStore) (T, error)) (T, error) {
	var zero T
	store, err := getStore()
	if err != nil {
		return zero, err
	}
	return fn(store)
}

func SaveMessage(sessionID string, msg Message) error {
	return SaveMessageContext(context.Background(), sessionID, msg)
}

func SaveMessageContext(ctx context.Context, sessionID string, msg Message) error {
	return withStore(func(s *JSONLStore) error { return s.SaveMessageContext(ctx, sessionID, msg) })
}

func LoadMessages(sessionID string) ([]Message, error) {
	return LoadMessagesContext(context.Background(), sessionID)
}

func LoadMessagesContext(ctx context.Context, sessionID string) ([]Message, error) {
	return withStoreVal(func(s *JSONLStore) ([]Message, error) { return s.LoadMessagesContext(ctx, sessionID) })
}

func HasMessages(sessionID string) bool {
	store, err := getStore()
	return err == nil && store.HasMessages(sessionID)
}

func OpenHistory(sessionID string) (*HistoryReader, error) {
	return OpenHistoryContext(context.Background(), sessionID)
}

func OpenHistoryContext(ctx context.Context, sessionID string) (*HistoryReader, error) {
	return withStoreVal(func(s *JSONLStore) (*HistoryReader, error) { return s.OpenHistoryContext(ctx, sessionID) })
}

func (s *JSONLStore) ForWorkspace(workspace string) *JSONLStore {
	return &JSONLStore{core: s.core, Workspace: workspace}
}

func (s *JSONLStore) ScopedStore(workspace string) session.ContextStore {
	return s.ForWorkspace(workspace)
}
