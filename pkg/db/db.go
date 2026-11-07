package db

import (
	"bufio"
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
	mu           sync.RWMutex
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
	_, err = w.Write(append(jsonData, '\n'))
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
	if isSkippedSlashRecord(msg) {
		return nil
	}
	return s.AppendEntry(sessionID, session.Record{Type: "message", Message: msg})
}

func (s *JSONLStore) LoadMessages(sessionID string) ([]message.Message, error) {
	if err := s.checkReady(sessionID); err != nil {
		return nil, err
	}

	entries, err := s.Entries(sessionID)

	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}

	projected := session.Project(entries)
	messages := make([]message.Message, 0, len(projected))
	for _, msg := range projected {
		if !isSkippedSlashRecord(msg) {
			messages = append(messages, msg)
		}
	}

	return messages, nil
}

func (s *JSONLStore) HasMessages(sessionID string) bool {
	if s.checkReady(sessionID) != nil {
		return false
	}
	filePath := s.sessionPath(sessionID)
	info, err := os.Stat(filePath)
	return err == nil && info.Size() > 0
}

func (s *JSONLStore) RewriteSession(sessionID string, messages []message.Message) error {
	return s.AppendEntry(sessionID, session.Record{Type: "context", Messages: messages})
}

func (s *JSONLStore) AppendEntry(sessionID string, record session.Record) error {
	if err := s.checkReady(sessionID); err != nil {
		return err
	}
	s.core.mu.Lock()
	defer s.core.mu.Unlock()
	release, err := s.lock(true)
	if err != nil {
		return err
	}
	defer release()
	cached, err := s.appendState(sessionID)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if cached.count > 0 && cached.lastType != "session" {
		record.ParentID = cached.lastID
	}
	// An interrupted append must never leave a reusable stale cache entry.
	delete(s.core.cache, sessionID)
	delete(s.core.summaryCache, sessionID)
	record.ID = session.NewUUID()
	record.Timestamp = nowTimestamp()
	f, err := os.OpenFile(s.sessionPath(sessionID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
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
	info, err := f.Stat()
	if err != nil {
		return err
	}
	cached.size, cached.modified = info.Size(), info.ModTime()
	cached.count++
	cached.lastID, cached.lastType = record.ID, record.Type
	if cached.complete {
		cached.entries = append(cached.entries, cloneRecord(record))
	}
	s.cacheEntries(sessionID, cached)
	return nil
}

func AppendEntry(sessionID string, record session.Record) error {
	return withStore(func(s *JSONLStore) error { return s.AppendEntry(sessionID, record) })
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
	return withStore(func(s *JSONLStore) error { return s.SaveMessage(sessionID, msg) })
}

func LoadMessages(sessionID string) ([]Message, error) {
	return withStoreVal(func(s *JSONLStore) ([]Message, error) { return s.LoadMessages(sessionID) })
}

func HasMessages(sessionID string) bool {
	store, err := getStore()
	return err == nil && store.HasMessages(sessionID)
}

func OpenHistory(sessionID string) (*HistoryReader, error) {
	return withStoreVal(func(s *JSONLStore) (*HistoryReader, error) { return s.OpenHistory(sessionID) })
}

func (s *JSONLStore) ForWorkspace(workspace string) *JSONLStore {
	return &JSONLStore{core: s.core, Workspace: workspace}
}
