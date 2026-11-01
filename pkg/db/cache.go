package db

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
)

// Limits apply to retained records, not explicit full-history reads. Large
// sessions retain only their append cursor, avoiding a full reread per save.
const (
	maxCachedSessions   = 8
	maxCachedRecords    = 500
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
	return c
}

func (s *JSONLStore) cached(id string, info os.FileInfo) (cachedEntries, bool) {
	c, ok := s.cache[id]
	if !ok {
		return c, false
	}
	if c.size != info.Size() || !c.modified.Equal(info.ModTime()) {
		delete(s.cache, id)
		return cachedEntries{}, false
	}
	s.cacheClock++
	c.used = s.cacheClock
	s.cache[id] = c
	return c, true
}

func (s *JSONLStore) cacheEntries(id string, c cachedEntries) {
	if c.count > maxCachedRecords || c.size > maxCachedFileBytes {
		c.entries = nil
		c.complete = false
	}
	s.cacheClock++
	c.used = s.cacheClock
	s.cache[id] = c
	for {
		var bytes int64
		oldest := ""
		var used uint64
		for key, entry := range s.cache {
			if entry.complete {
				bytes += entry.size
			}
			if oldest == "" || entry.used < used {
				oldest, used = key, entry.used
			}
		}
		if len(s.cache) <= maxCachedSessions && bytes <= maxCachedTotalBytes {
			return
		}
		delete(s.cache, oldest)
	}
}

func (s *JSONLStore) appendState(id string) (cachedEntries, error) {
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
	f, err := os.Open(s.sessionPath(id))
	if err != nil {
		return cachedEntries{}, err
	}
	defer f.Close()
	c := cachedEntries{size: info.Size(), modified: info.ModTime(), complete: info.Size() <= maxCachedFileBytes}
	// Validate envelopes one at a time. Bodies are released immediately once
	// the retention limit is exceeded; only IDs are needed for parent checks.
	ids := make(map[string]bool)
	scanner := newJSONLScanner(f)
	line := 0
	for scanner.Scan() {
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
		} else if entry.ID == "" {
			ids[fmt.Sprintf("legacy-%d", c.count)] = true
		} else {
			if ids[entry.ID] {
				return cachedEntries{}, fmt.Errorf("duplicate session entry %q", entry.ID)
			}
			if entry.ParentID != "" && !ids[entry.ParentID] {
				return cachedEntries{}, fmt.Errorf("entry %q has unknown or forward parent %q", entry.ID, entry.ParentID)
			}
			ids[entry.ID] = true
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
	s.cacheEntries(id, c)
	return c, nil
}
