package config

import (
	"loop/pkg/domain/model"
	"sort"
)

func (c *Config) Models() []model.Model {
	var result []model.Model
	for provider, profile := range c.Providers {
		seen := make(map[string]bool)
		for _, entry := range profile.Models {
			entry.Provider = provider
			if entry.API == "" {
				entry.API = profile.API
			}
			result = append(result, entry)
			seen[entry.ID] = true
		}
		if profile.Model != "" && !seen[profile.Model] {
			result = append(result, model.Model{ID: profile.Model, Provider: provider, API: profile.API})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Provider == result[j].Provider {
			return result[i].ID < result[j].ID
		}
		return result[i].Provider < result[j].Provider
	})
	return result
}
