package repl

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"loop/pkg/config"
)

// manualShell owns the directory of user-entered commands. Project resources,
// file-tool boundaries and session storage keep their original workspace.
type manualShell struct {
	directory string
}

func (s *manualShell) changeDirectory(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty directory")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.directory, path)
	}
	directory, err := config.CanonicalWorkspace(path)
	if err != nil {
		return "", err
	}
	s.directory = directory
	return directory, nil
}

// directoryCommand recognizes a literal cd without evaluating shell code.
// Commands containing operators or expansions continue through Bash normally.
func directoryCommand(command string) (string, bool, error) {
	command = strings.TrimLeft(command, " \t")
	if command != "cd" && !strings.HasPrefix(command, "cd ") && !strings.HasPrefix(command, "cd\t") {
		return "", false, nil
	}
	var words []string
	var word strings.Builder
	var quote byte
	started, expandHome := false, false
	for i := 2; i < len(command); i++ {
		ch := command[i]
		if quote != '\'' && strings.ContainsRune("$`", rune(ch)) {
			return "", false, nil
		}
		if ch == '\\' && quote != '\'' {
			if i+1 == len(command) {
				return "", true, fmt.Errorf("unfinished escape")
			}
			next := command[i+1]
			if quote == '"' && !strings.ContainsRune("$`\"\\\n", rune(next)) {
				word.WriteByte(ch)
				started = true
				continue
			}
			i++
			if next != '\n' {
				word.WriteByte(next)
				started = true
			}
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else {
				word.WriteByte(ch)
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quote, started = ch, true
		case ' ', '\t':
			if started {
				words = append(words, word.String())
				word.Reset()
				started = false
			}
		default:
			if strings.ContainsRune(";&|<>()\n\r*?[]{}#", rune(ch)) {
				return "", false, nil
			}
			if !started && ch == '~' {
				expandHome = true
			}
			word.WriteByte(ch)
			started = true
		}
	}
	if quote != 0 {
		return "", true, fmt.Errorf("unclosed quote")
	}
	if started {
		words = append(words, word.String())
	}
	if len(words) > 0 && words[0] == "--" {
		words = words[1:]
	}
	if len(words) > 1 {
		return "", true, fmt.Errorf("too many arguments")
	}
	if len(words) == 0 {
		home, err := os.UserHomeDir()
		return home, true, err
	}
	path := words[0]
	if expandHome && (path == "~" || strings.HasPrefix(path, "~/")) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", true, err
		}
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	}
	return path, true, nil
}
