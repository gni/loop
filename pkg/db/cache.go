package db

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
	"loop/pkg/storage"
)

// Limits apply to retained records, not explicit full-history reads. Large
// sessions retain only their append cursor, avoiding a full reread per save.
const (
	maxCachedSessions   = 8
	maxCachedRecords    = 4096
	maxCachedFileBytes  = 4 << 20
	maxCachedTotalBytes = 8 << 20
)

type cachedEntries struct {
	size             int64
	modified         time.Time
	entries          []session.Record
	complete         bool
	count            int
	lastID, lastType string
	settingsHash     *[sha256.Size]byte
	used             uint64
}

func cloneRecord(entry session.Record) session.Record {
	entry.Message = message.Clone(entry.Message)
	entry.Messages = message.CloneAll(entry.Messages)
	entry.Data = append(entry.Data[:0:0], entry.Data...)
	return entry
}

func cacheForEntries(info os.FileInfo, entries []session.Record) cachedEntries {
	c := cachedEntries{size: info.Size(), modified: info.ModTime(), count: len(entries), complete: true, entries: entries}
	if len(entries) > 0 {
		last := entries[len(entries)-1]
		c.lastID, c.lastType = last.ID, last.Type
		if c.lastID == "" {
			c.lastID = fmt.Sprintf("legacy-%d", len(entries)-1)
		}
	}
	path := session.LeafPath(entries, "")
	for i := len(path) - 1; i >= 0; i-- {
		if path[i].Type == "settings" {
			hash := sha256.Sum256(path[i].Data)
			c.settingsHash = &hash
			break
		}
	}
	return c
}

func (s *JSONLStore) cached(id string, info os.FileInfo) (cachedEntries, bool) {
	c, ok := s.core.cache[id]
	if !ok {
		return c, false
	}
	if c.size != info.Size() || !c.modified.Equal(info.ModTime()) {
		delete(s.core.cache, id)
		return cachedEntries{}, false
	}
	s.core.cacheClock++
	c.used = s.core.cacheClock
	s.core.cache[id] = c
	return c, true
}

func (s *JSONLStore) cacheEntries(id string, c cachedEntries) {
	if c.count > maxCachedRecords || c.size > maxCachedFileBytes {
		c.entries = nil
		c.complete = false
	}
	s.core.cacheClock++
	c.used = s.core.cacheClock
	s.core.cache[id] = c
	for {
		var bytes int64
		oldest := ""
		var used uint64
		for key, entry := range s.core.cache {
			if entry.complete {
				bytes += entry.size
			}
			if oldest == "" || entry.used < used {
				oldest, used = key, entry.used
			}
		}
		if len(s.core.cache) <= maxCachedSessions && bytes <= maxCachedTotalBytes {
			return
		}
		delete(s.core.cache, oldest)
	}
}

func (s *JSONLStore) appendState(id string) (cachedEntries, error) {
	return s.appendStateContext(context.Background(), id)
}

func (s *JSONLStore) appendStateContext(ctx context.Context, id string) (cachedEntries, error) {
	info, err := os.Stat(s.sessionPath(id))
	if os.IsNotExist(err) {
		return cachedEntries{complete: true}, err
	}
	if err != nil {
		return cachedEntries{}, err
	}
	if c, ok := s.cached(id, info); ok {
		return c, nil
	}
	f, err := storage.OpenRegularFile(s.sessionPath(id))
	if err != nil {
		return cachedEntries{}, err
	}
	defer f.Close()
	c := cachedEntries{size: info.Size(), modified: info.ModTime(), complete: info.Size() <= maxCachedFileBytes}
	// Validate envelopes one at a time. Bodies are released immediately once
	// the retention limit is exceeded. Settings digests follow parent links so
	// deduplication uses the active branch even after a branch-marker append.
	ids := make(map[string]*[sha256.Size]byte)
	var previous string
	scanner := newJSONLScanner(contextReader{ctx, f})
	line := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return cachedEntries{}, err
		}
		line++
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var entry session.Record
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return cachedEntries{}, fmt.Errorf("session record %d is corrupt: %w", line, err)
		}
		if entry.Type == "session" {
			if c.count != 0 || entry.Version > session.CurrentSessionVersion {
				return cachedEntries{}, fmt.Errorf("unsupported session header at entry %d", c.count)
			}
		} else {
			id, parent := entry.ID, entry.ParentID
			if id == "" {
				id, parent = fmt.Sprintf("legacy-%d", c.count), previous
			}
			if _, exists := ids[id]; exists {
				return cachedEntries{}, fmt.Errorf("duplicate session entry %q", entry.ID)
			}
			hash, exists := ids[parent]
			if parent != "" && !exists {
				return cachedEntries{}, fmt.Errorf("entry %q has unknown or forward parent %q", id, parent)
			}
			if entry.Type == "settings" {
				digest := sha256.Sum256(entry.Data)
				hash = &digest
			}
			ids[id] = hash
			c.settingsHash = hash
			if entry.Type != "branch" {
				previous = id
			}
		}
		c.lastID, c.lastType = entry.ID, entry.Type
		if c.lastID == "" {
			c.lastID = fmt.Sprintf("legacy-%d", c.count)
		}
		c.count++
		if c.complete {
			if c.count > maxCachedRecords {
				c.complete = false
				c.entries = nil
			} else {
				c.entries = append(c.entries, entry)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return cachedEntries{}, fmt.Errorf("read session append cursor: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return cachedEntries{}, err
	}
	s.cacheEntries(id, c)
	return c, nil
}

// invalidateSessionCacheLocked keeps record and summary caches consistent.
// The caller must hold core.mu for writing.
func (s *JSONLStore) invalidateSessionCacheLocked(sessionID string) {
	delete(s.core.cache, sessionID)
	delete(s.core.summaryCache, sessionID)
}
