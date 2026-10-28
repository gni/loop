package model

import (
	"fmt"
	"sort"
	"strings"
)

type Pricing struct {
	Input      float64  `json:"input"`
	Output     float64  `json:"output"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
}

type Model struct {
	ID            string   `json:"id"`
	Provider      string   `json:"provider,omitempty"`
	API           string   `json:"api,omitempty"`
	ContextWindow int      `json:"context_window,omitempty"`
	MaxOutput     int      `json:"max_output,omitempty"`
	Input         []string `json:"input,omitempty"`
	Reasoning     []string `json:"reasoning,omitempty"`
	Cache         bool     `json:"cache,omitempty"`
	Pricing       *Pricing `json:"pricing,omitempty"`
}

func Resolve(models []Model, provider, query string) (Model, error) {
	var matches []Model
	query = strings.TrimSpace(query)
	for _, candidate := range models {
		if provider != "" && candidate.Provider != provider {
			continue
		}
		if candidate.ID == query {
			return candidate, nil
		}
		if query != "" && strings.Contains(strings.ToLower(candidate.ID), strings.ToLower(query)) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		names := make([]string, len(matches))
		for i, entry := range matches {
			names[i] = entry.ID
		}
		sort.Strings(names)
		return Model{}, fmt.Errorf("ambiguous model %q: %s", query, strings.Join(names, ", "))
	}
	return Model{}, fmt.Errorf("model %q is not registered", query)
}

func (m Model) Supports(input string) bool {
	if len(m.Input) == 0 {
		return true
	}
	for _, modality := range m.Input {
		if modality == input {
			return true
		}
	}
	return false
}

func (p Pricing) Cost(input, output, cacheRead, cacheWrite int) *float64 {
	if (cacheRead > 0 && p.CacheRead == nil) || (cacheWrite > 0 && p.CacheWrite == nil) {
		return nil
	}
	cost := float64(max(0, input-cacheRead-cacheWrite))*p.Input + float64(output)*p.Output
	if p.CacheRead != nil {
		cost += float64(cacheRead) * *p.CacheRead
	}
	if p.CacheWrite != nil {
		cost += float64(cacheWrite) * *p.CacheWrite
	}
	cost /= 1_000_000
	return &cost
}
