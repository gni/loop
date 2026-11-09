package config

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"
)

// ValidateSettings shares value constraints across persisted, runtime and
// interactive settings. Zero remains valid for defaults in optional file keys.
func (cfg *Config) ValidateSettings() error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if err := cfg.ValidateTimeouts(); err != nil {
		return err
	}
	if err := cfg.ValidatePrompt(); err != nil {
		return err
	}
	if err := cfg.Nostop.Validate(); err != nil {
		return err
	}
	value := reflect.ValueOf(cfg).Elem()
	for _, setting := range settingsSchema {
		if setting.key == "recap_interval" {
			continue
		}
		field := value.FieldByIndex(setting.field.Index)
		if field.Kind() == reflect.Int && field.Int() < 0 {
			return fmt.Errorf("%s must be non-negative", setting.key)
		}
	}
	if cfg.LogoBrightness != 0 && (cfg.LogoBrightness < 10 || cfg.LogoBrightness > 100) {
		return fmt.Errorf("logo_brightness must be between 10 and 100")
	}
	if math.IsNaN(cfg.Temperature) || math.IsInf(cfg.Temperature, 0) || cfg.Temperature < 0 || cfg.Temperature > 2 {
		return fmt.Errorf("temperature must be between 0 and 2")
	}
	for _, penalty := range []struct {
		name  string
		value float64
	}{{"frequency_penalty", cfg.FrequencyPenalty}, {"presence_penalty", cfg.PresencePenalty}} {
		if math.IsNaN(penalty.value) || math.IsInf(penalty.value, 0) || penalty.value < -2 || penalty.value > 2 {
			return fmt.Errorf("%s must be between -2 and 2", penalty.name)
		}
	}
	if cfg.CompressionThreshold != 0 {
		if _, err := ParseCompressionThreshold(fmt.Sprint(cfg.CompressionThreshold)); err != nil {
			return fmt.Errorf("compression_threshold: %w", err)
		}
	}
	if cfg.ReasoningEffort != "" {
		if _, err := ParseReasoningEffort(cfg.ReasoningEffort); err != nil {
			return err
		}
	}
	if cfg.AskUserMode != "" {
		if _, err := ParseAskUserMode(cfg.AskUserMode); err != nil {
			return err
		}
	}
	if protocol := strings.ToLower(strings.TrimSpace(cfg.ToolProtocol)); protocol != "" && protocol != "json" && protocol != "xml" {
		return fmt.Errorf("tool_protocol must be json or xml")
	}
	return nil
}

func validateTimeoutValue(key string, seconds int64) error {
	if seconds < 0 || seconds > math.MaxInt64/int64(time.Second) {
		return fmt.Errorf("%s must be between 0 and %d seconds", key, math.MaxInt64/int64(time.Second))
	}
	return nil
}

func (cfg *Config) ValidateTimeouts() error {
	for _, setting := range []struct {
		key   string
		value int
	}{{"timeout", cfg.Timeout}, {"shell_timeout", cfg.ShellTimeout}, {"extension_timeout", cfg.ExtensionTimeout}} {
		if err := validateTimeoutValue(setting.key, int64(setting.value)); err != nil {
			return err
		}
	}
	for name, provider := range cfg.Providers {
		if err := validateTimeoutValue("provider "+name+" timeout", int64(provider.Timeout)); err != nil {
			return err
		}
	}
	return nil
}
