package config

import "fmt"

func (c *Config) PromptRows() int {
	if c.PromptMaxLines <= 0 {
		return 8
	}
	return c.PromptMaxLines
}

func (c *Config) ValidatePrompt() error {
	if c.PromptMaxLines < 0 {
		return fmt.Errorf("prompt_max_lines must be positive, or zero for the default")
	}
	return nil
}
