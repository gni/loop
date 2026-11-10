package config

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

func saveProjectConfig(ctx context.Context, cfg *Config) error {
	if cfg.ProjectBase == nil {
		return fmt.Errorf("project configuration has no base settings")
	}
	current, err := Clone(cfg)
	if err != nil {
		return err
	}
	current.UpdateActiveProvider()
	values := projectValue(reflect.ValueOf(current)).(map[string]any)
	base := projectValue(reflect.ValueOf(cfg.ProjectBase)).(map[string]any)
	delta, changed := projectDifference(values, base)
	patch := make(map[string]any)
	if changed {
		patch = delta.(map[string]any)
	}
	// Inherited connection secrets stay in private global settings. Repository
	// settings retain environment references rather than materialized API keys.
	stripProjectKey(patch)
	if providers, ok := patch["providers"].(map[string]any); ok {
		for name, value := range providers {
			if profile, ok := value.(map[string]any); ok {
				stripProjectKey(profile)
				if len(profile) == 0 {
					delete(providers, name)
				}
			}
		}
		if len(providers) == 0 {
			delete(patch, "providers")
		}
	}
	data, err := json.MarshalIndent(patch, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 8<<20 {
		return fmt.Errorf("project configuration exceeds 8 MiB")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return WritePrivateFile(cfg.ProjectPath, data)
}

func stripProjectKey(values map[string]any) {
	if key, ok := values["api_key"].(string); ok && key != "" {
		if _, reference := parseEnvVarKey(key); !reference {
			delete(values, "api_key")
		}
	}
}

// Settings deltas need explicit zero values: omitempty would turn a cleared
// string or integer into an absent key and restore the inherited value.
func projectValue(value reflect.Value) any {
	for value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Struct:
		result := make(map[string]any)
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			key := strings.Split(field.Tag.Get("json"), ",")[0]
			if !field.IsExported() || key == "" || key == "-" {
				continue
			}
			result[key] = projectValue(value.Field(i))
		}
		return result
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		result := make(map[string]any, value.Len())
		for iter := value.MapRange(); iter.Next(); {
			result[iter.Key().String()] = projectValue(iter.Value())
		}
		return result
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return nil
		}
		result := make([]any, value.Len())
		for i := range result {
			result[i] = projectValue(value.Index(i))
		}
		return result
	default:
		return value.Interface()
	}
}

func projectDifference(current, base any) (any, bool) {
	if reflect.DeepEqual(current, base) {
		return nil, false
	}
	values, currentObject := current.(map[string]any)
	inherited, baseObject := base.(map[string]any)
	if !currentObject || !baseObject {
		return current, true
	}
	patch := make(map[string]any)
	for key, value := range values {
		if change, changed := projectDifference(value, inherited[key]); changed {
			patch[key] = change
		}
	}
	for key := range inherited {
		if _, exists := values[key]; !exists {
			patch[key] = nil
		}
	}
	return patch, len(patch) > 0
}
