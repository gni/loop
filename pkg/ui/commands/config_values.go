package commands

import (
	"fmt"
	"io"
	"strconv"

	"loop/pkg/config"
)

// Edit a candidate configuration; runtime effects happen only after validation.
func setConfigValue(cfg *config.Config, key, value string, w io.Writer) bool {
	key, value, err := config.CanonicalSetting(key, value)
	if err != nil {
		fmt.Fprintln(w, err)
		return false
	}
	switch key {
	case "compaction_reserve_tokens":
		var reserve int
		reserve, err = strconv.Atoi(value)
		if err == nil && reserve <= 0 {
			err = fmt.Errorf("compaction reserve tokens must be positive")
		}
		if err == nil {
			cfg.Limits.CompactionReserveTokens = reserve
		}
	case "disabled_tools":
		cfg.DisabledTools = config.ParseToolList(value)
	default:
		err = cfg.SetValue(key, value)
	}
	if err != nil {
		fmt.Fprintln(w, err)
		return false
	}
	if key == "endpoint" || key == "model" || key == "api_key" || key == "timeout" {
		cfg.UpdateActiveProvider()
	}
	return true
}
