package ui

import (
	"strings"
	"sync"
)

type CustomHistory struct {
	mu      sync.RWMutex
	entries []string
	fullMap map[string]string
}

type customHistory = CustomHistory

// NewCustomHistory creates an empty thread-safe CustomHistory.
func NewCustomHistory() *CustomHistory {
	return &CustomHistory{}
}

// Clear empties all entries and full mappings in the history.
func (h *CustomHistory) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = nil
	h.fullMap = nil
}

// Deduplicate removes duplicate entries from history, preserving latest order.
func Deduplicate(entries []string) []string {
	seen := make(map[string]bool)
	var unique []string
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if !seen[entry] {
			seen[entry] = true
			unique = append([]string{entry}, unique...)
		}
	}
	return unique
}

func (h *customHistory) Add(entry string) {
	raw := strings.TrimSpace(entry)
	if raw == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fullMap == nil {
		h.fullMap = make(map[string]string)
	}

	display := strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", " ↵ "), "\n", " ↵ ")
	display = strings.TrimSpace(display)

	h.fullMap[display] = raw
	h.fullMap[raw] = raw

	h.entries = append(h.entries, display)
	h.entries = Deduplicate(h.entries)
}

func (h *customHistory) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.entries)
}

func (h *customHistory) At(idx int) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if idx < 0 || idx >= len(h.entries) {
		return ""
	}
	return h.entries[len(h.entries)-1-idx]
}

func (h *customHistory) GetFull(line string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.fullMap == nil {
		return line
	}
	if full, ok := h.fullMap[line]; ok && full != "" {
		return full
	}
	return line
}
