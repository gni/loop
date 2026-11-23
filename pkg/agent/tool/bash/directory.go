package bash

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"loop/pkg/storage"
)

const (
	maxPersistedEnvVars  = 128
	maxPersistedEnvBytes = 64 << 10
	maxEnvCaptureBytes   = 256 << 10
)

type shellStateCapture struct {
	cwdPath string
	envPath string
}

// Keep shell state out of stdout, including redirected and truncated output.
func captureShellState(command string) (string, *shellStateCapture, error) {
	cwdFile, err := os.CreateTemp("", "loop-cwd-*")
	if err != nil {
		return "", nil, fmt.Errorf("create shell directory capture: %w", err)
	}
	cwdPath := cwdFile.Name()
	if err := cwdFile.Close(); err != nil {
		_ = os.Remove(cwdPath)
		return "", nil, err
	}

	envFile, err := os.CreateTemp("", "loop-env-*")
	if err != nil {
		_ = os.Remove(cwdPath)
		return "", nil, fmt.Errorf("create shell environment capture: %w", err)
	}
	envPath := envFile.Name()
	if err := envFile.Close(); err != nil {
		_ = os.Remove(cwdPath)
		_ = os.Remove(envPath)
		return "", nil, err
	}

	capture := &shellStateCapture{cwdPath: cwdPath, envPath: envPath}
	trap := "builtin pwd -P >| " + ShellQuote(capture.cwdPath) + " || :; env -0 >| " + ShellQuote(capture.envPath) + " 2>/dev/null || :"
	return "trap " + ShellQuote(trap) + " EXIT\n" + command, capture, nil
}

func (c *shellStateCapture) ReadDir(ctx context.Context) string {
	data, err := storage.ReadFileContext(ctx, c.cwdPath, 64<<10)
	if err != nil {
		return ""
	}
	// pwd adds exactly one newline. Other whitespace belongs to the path.
	return strings.TrimSuffix(string(data), "\n")
}

func (c *shellStateCapture) ReadEnv(ctx context.Context) (map[string]string, bool) {
	data, err := storage.ReadFileContext(ctx, c.envPath, maxEnvCaptureBytes)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	parsed := make(map[string]string)
	for _, entry := range bytes.Split(data, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		k, v, ok := strings.Cut(string(entry), "=")
		if !ok || !isValidEnvName(k) {
			continue
		}
		parsed[k] = v
	}
	return parsed, len(parsed) > 0
}

func (c *shellStateCapture) Close() {
	if c == nil {
		return
	}
	_ = os.Remove(c.cwdPath)
	_ = os.Remove(c.envPath)
}

func isValidEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if i == 0 && !(ch == '_' || (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z')) {
			return false
		}
		if !(ch == '_' || (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')) {
			return false
		}
	}
	return true
}

func isEphemeralShellVar(name string) bool {
	switch name {
	case "PWD", "OLDPWD", "SHLVL", "_", "RANDOM", "SECONDS", "LINENO",
		"HISTCMD", "OPTIND", "OPTARG", "PIPESTATUS", "FUNCNAME", "GROUPS",
		"EPOCHSECONDS", "EPOCHREALTIME", "SRANDOM",
		"LC_ALL", "LANG", "PYTHONUNBUFFERED",
		"GIT_TERMINAL_PROMPT", "GCM_INTERACTIVE", "DEBIAN_FRONTEND",
		"LD_PRELOAD", "LD_LIBRARY_PATH":
		return true
	}
	return strings.HasPrefix(name, "BASH_") || strings.HasPrefix(name, "LOOP_")
}

func envSliceToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, item := range env {
		if k, v, ok := strings.Cut(item, "="); ok && k != "" {
			m[k] = v
		}
	}
	return m
}

func applyPersistentEnv(baseEnv []string, persisted map[string]string, unset map[string]bool) []string {
	if len(persisted) == 0 && len(unset) == 0 {
		return baseEnv
	}
	merged := envSliceToMap(baseEnv)
	for k := range unset {
		delete(merged, k)
	}
	for k, v := range persisted {
		merged[k] = v
	}
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+merged[k])
	}
	return out
}

func (t *bashTool) updatePersistedEnv(beforeEnv []string, afterEnv map[string]string) {
	if len(afterEnv) == 0 {
		return
	}
	before := envSliceToMap(beforeEnv)
	if t.envVars == nil {
		t.envVars = make(map[string]string)
	}
	if t.unsetVars == nil {
		t.unsetVars = make(map[string]bool)
	}
	totalBytes := 0
	for k, v := range t.envVars {
		totalBytes += len(k) + len(v)
	}
	for k, v := range afterEnv {
		if isEphemeralShellVar(k) {
			continue
		}
		if prev, existed := before[k]; existed && prev == v {
			continue
		}
		delete(t.unsetVars, k)
		if old, had := t.envVars[k]; had {
			totalBytes -= len(k) + len(old)
		} else if len(t.envVars) >= maxPersistedEnvVars {
			continue
		}
		if totalBytes+len(k)+len(v) > maxPersistedEnvBytes {
			continue
		}
		t.envVars[k] = v
		totalBytes += len(k) + len(v)
	}
	for k := range before {
		if isEphemeralShellVar(k) {
			continue
		}
		if _, exists := afterEnv[k]; !exists {
			delete(t.envVars, k)
			if len(t.unsetVars) < maxPersistedEnvVars {
				t.unsetVars[k] = true
			}
		}
	}
}
