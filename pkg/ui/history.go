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

func NewCustomHistory() *CustomHistory {
	return &CustomHistory{}
}

func (h *CustomHistory) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = nil
	h.fullMap = nil
}

func Deduplicate(entries []string) []string {
	seen := make(map[string]bool, len(entries))
	var unique []string
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if !seen[entry] {
			seen[entry] = true
			unique = append(unique, entry)
		}
	}
	for i, j := 0, len(unique)-1; i < j; i, j = i+1, j-1 {
		unique[i], unique[j] = unique[j], unique[i]
	}
	return unique
}

func (h *CustomHistory) Add(entry string) {
	raw := strings.TrimSpace(entry)
	if raw == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fullMap == nil {
		h.fullMap = make(map[string]string)
	}

	display := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", " ↵ "), "\n", " ↵ "))
	h.fullMap[display] = raw
	h.fullMap[raw] = raw
	h.entries = Deduplicate(append(h.entries, display))
}

func (h *CustomHistory) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.entries)
}

func (h *CustomHistory) At(idx int) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if idx < 0 || idx >= len(h.entries) {
		return ""
	}
	return h.entries[len(h.entries)-1-idx]
}

func (h *CustomHistory) GetFull(line string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if full := h.fullMap[line]; full != "" {
		return full
	}
	return line
}