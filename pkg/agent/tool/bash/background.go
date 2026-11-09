package bash

import "strings"

// backgroundCommand only removes a final top-level job-control wrapper. The
// scanner understands quoting and nested expansions; unsupported constructs,
// including heredocs, are passed to Bash unchanged rather than guessed at.
func backgroundCommand(command string) (string, bool) {
	ampersand, complete := backgroundAmpersand(command)
	if !complete {
		return command, false
	}
	if ampersand >= 0 {
		tail := strings.TrimSpace(command[ampersand+1:])
		if tail == "" || tail == "echo $!" || tail == "echo \"$!\"" {
			normalized := strings.TrimRight(command[:ampersand], " \t\r\n")
			if strings.TrimSpace(normalized) != "" {
				return normalized, true
			}
		}
		return command, false
	}
	trimmed := strings.TrimLeft(command, " \t\r\n")
	return command, strings.HasPrefix(trimmed, "nohup ") || strings.HasPrefix(trimmed, "nohup\t")
}

type shellScanFrame struct {
	end, quote                  byte
	ansi, arithmetic, parameter bool
}

func backgroundAmpersand(command string) (int, bool) {
	frames := []shellScanFrame{{}}
	ampersand := -1
	for i := 0; i < len(command); i++ {
		frame := &frames[len(frames)-1]
		ch := command[i]
		if frame.quote == '\'' {
			if frame.ansi && ch == '\\' {
				i++
			} else if ch == '\'' {
				frame.quote, frame.ansi = 0, false
			}
			continue
		}
		if ch == '\\' {
			i++
			continue
		}
		if ch == '$' && i+1 < len(command) {
			switch command[i+1] {
			case '(':
				arithmetic := i+2 < len(command) && command[i+2] == '('
				frames = append(frames, shellScanFrame{end: ')', arithmetic: arithmetic})
				i++
				if arithmetic {
					frames = append(frames, shellScanFrame{end: ')', arithmetic: true})
					i++
				}
				continue
			case '{':
				frames = append(frames, shellScanFrame{end: '}', parameter: true})
				i++
				continue
			case '\'', '"':
				if frame.quote == 0 {
					frame.quote, frame.ansi = command[i+1], command[i+1] == '\''
					i++
					continue
				}
			}
		}
		if ch == '`' {
			if frame.end == '`' && frame.quote == 0 {
				frames = frames[:len(frames)-1]
			} else {
				frames = append(frames, shellScanFrame{end: '`'})
			}
			continue
		}
		if frame.quote == '"' {
			if ch == '"' {
				frame.quote = 0
			}
			continue
		}
		if frame.end != 0 && ch == frame.end {
			frames = frames[:len(frames)-1]
			continue
		}
		switch ch {
		case '\'', '"':
			frame.quote = ch
		case '(':
			frames = append(frames, shellScanFrame{end: ')', arithmetic: frame.arithmetic})
		case '{':
			if (i == 0 || shellWordBoundary(command[i-1])) && (i+1 == len(command) || shellWordBoundary(command[i+1])) {
				frames = append(frames, shellScanFrame{end: '}'})
			}
		case ')':
			return -1, false
		case '<':
			if !frame.arithmetic && !frame.parameter && i+1 < len(command) && command[i+1] == '<' {
				return -1, false
			}
		case '#':
			if !frame.parameter && !frame.arithmetic && (i == 0 || shellWordBoundary(command[i-1])) {
				if end := strings.IndexByte(command[i:], '\n'); end >= 0 {
					i += end
				} else {
					i = len(command)
				}
			}
		case '&':
			if len(frames) != 1 || (i > 0 && strings.ContainsRune("&<>|", rune(command[i-1]))) ||
				(i+1 < len(command) && strings.ContainsRune("&>", rune(command[i+1]))) {
				continue
			}
			if ampersand >= 0 {
				return -1, false
			}
			ampersand = i
		}
	}
	return ampersand, len(frames) == 1 && frames[0].quote == 0
}

func shellWordBoundary(ch byte) bool {
	return strings.ContainsRune(" \t\r\n;&|()<>", rune(ch))
}

// CleanBackgroundCommand preserves command arguments and nohup options.
func CleanBackgroundCommand(command string) string {
	normalized, _ := backgroundCommand(command)
	return normalized
}
