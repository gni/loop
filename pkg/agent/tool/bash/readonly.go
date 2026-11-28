package bash

import (
	"path/filepath"
	"strings"

	domaintool "loop/pkg/domain/tool"
)

// IsReadOnlyArguments reports whether a bash tool invocation is strictly a
// single-line read-only inspection without backgrounding, directory mutation,
// file redirection, command substitution, or mutating commands.
func IsReadOnlyArguments(arguments string) bool {
	var args struct {
		Command    string `json:"command"`
		Cmd        string `json:"cmd"`
		Arguments  string `json:"arguments"`
		Dir        string `json:"dir"`
		Cwd        string `json:"cwd"`
		Background *bool  `json:"background"`
	}
	if err := domaintool.ParseToolArguments(arguments, &args, func(s string) { args.Command = s }); err != nil {
		return false
	}
	if args.Background != nil && *args.Background {
		return false
	}
	if strings.TrimSpace(domaintool.FirstNonEmpty(args.Dir, args.Cwd)) != "" {
		return false
	}
	cmd := domaintool.FirstNonEmpty(args.Command, args.Cmd, args.Arguments)
	if _, bg := backgroundCommand(cmd); bg {
		return false
	}
	return IsReadOnlyCommand(cmd)
}

// IsReadOnlyCommand conservatively classifies simple single-line shell
// pipelines and lists whose commands only inspect state without mutating
// files, processes, or persistent shell environment/working directory.
func IsReadOnlyCommand(command string) bool {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" || strings.ContainsAny(trimmed, "\r\n") {
		return false
	}
	segments, ok := splitReadOnlyShellSegments(trimmed)
	if !ok || len(segments) == 0 {
		return false
	}
	for _, seg := range segments {
		if !isReadOnlySimpleCommand(seg) {
			return false
		}
	}
	return true
}

func splitReadOnlyShellSegments(s string) ([][]string, bool) {
	var segments [][]string
	var curSeg []string
	var word strings.Builder
	wordStarted := false
	inSingle := false
	inDouble := false
	expectInputTarget := false

	flushWord := func() bool {
		if !wordStarted {
			return true
		}
		token := word.String()
		word.Reset()
		wordStarted = false
		if expectInputTarget {
			expectInputTarget = false
			return token != ""
		}
		curSeg = append(curSeg, token)
		return true
	}

	flushSegment := func() bool {
		if !flushWord() || expectInputTarget || len(curSeg) == 0 {
			return false
		}
		segments = append(segments, curSeg)
		curSeg = nil
		return true
	}

	for i := 0; i < len(s); {
		ch := s[i]
		if inSingle {
			if ch == '\'' {
				inSingle = false
			} else {
				word.WriteByte(ch)
			}
			i++
			continue
		}
		if inDouble {
			if ch == '\\' {
				if i+1 >= len(s) {
					return nil, false
				}
				word.WriteByte(s[i+1])
				i += 2
				continue
			}
			if ch == '"' {
				inDouble = false
				i++
				continue
			}
			if ch == '`' || (ch == '$' && i+1 < len(s) && (s[i+1] == '(' || s[i+1] == '{')) {
				return nil, false
			}
			word.WriteByte(ch)
			i++
			continue
		}

		switch ch {
		case ' ', '\t':
			if !flushWord() {
				return nil, false
			}
			i++
		case '\'':
			inSingle = true
			wordStarted = true
			i++
		case '"':
			inDouble = true
			wordStarted = true
			i++
		case '\\':
			if i+1 >= len(s) {
				return nil, false
			}
			word.WriteByte(s[i+1])
			wordStarted = true
			i += 2
		case '`', '(', ')', '{', '}':
			return nil, false
		case '$':
			if i+1 < len(s) && (s[i+1] == '(' || s[i+1] == '{' || s[i+1] == '\'' || s[i+1] == '"') {
				return nil, false
			}
			word.WriteByte(ch)
			wordStarted = true
			i++
		case '#':
			if !wordStarted {
				i = len(s)
				continue
			}
			word.WriteByte(ch)
			i++
		case ';':
			if !flushSegment() {
				return nil, false
			}
			i++
		case '|':
			if !flushSegment() {
				return nil, false
			}
			if i+1 < len(s) && s[i+1] == '|' {
				i += 2
			} else {
				i++
			}
		case '&':
			if i+1 < len(s) && s[i+1] == '&' {
				if !flushSegment() {
					return nil, false
				}
				i += 2
				continue
			}
			return nil, false
		case '<':
			if expectInputTarget {
				return nil, false
			}
			if i+1 < len(s) && (s[i+1] == '<' || s[i+1] == '>' || s[i+1] == '(' || s[i+1] == '&') {
				return nil, false
			}
			if !flushWord() {
				return nil, false
			}
			expectInputTarget = true
			i++
		case '>':
			if expectInputTarget {
				return nil, false
			}
			// Permit harmless stream merges (2>&1, 1>&2) and /dev/null sinks only.
			fd := ""
			if wordStarted {
				w := word.String()
				if w == "1" || w == "2" {
					fd = w
					word.Reset()
					wordStarted = false
				} else if !flushWord() {
					return nil, false
				}
			}
			if i+1 < len(s) && s[i+1] == '&' {
				if (fd == "2" || fd == "1") && i+2 < len(s) && (s[i+2] == '1' || s[i+2] == '2') {
					if i+3 >= len(s) || isShellSeparatorOrSpace(s[i+3]) {
						i += 3
						continue
					}
				}
				return nil, false
			}
			if i+1 < len(s) && (s[i+1] == '>' || s[i+1] == '|' || s[i+1] == '(') {
				return nil, false
			}
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			const devNull = "/dev/null"
			if strings.HasPrefix(s[j:], devNull) {
				after := j + len(devNull)
				if after >= len(s) || isShellSeparatorOrSpace(s[after]) {
					i = after
					continue
				}
			}
			return nil, false
		default:
			word.WriteByte(ch)
			wordStarted = true
			i++
		}
	}

	if inSingle || inDouble || expectInputTarget {
		return nil, false
	}
	if len(curSeg) > 0 || wordStarted {
		if !flushSegment() {
			return nil, false
		}
	}
	return segments, len(segments) > 0
}

func isShellSeparatorOrSpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == ';' || ch == '|' || ch == '&'
}

func isSafeReadOnlyEnvAssignment(token string) bool {
	eq := strings.IndexByte(token, '=')
	if eq <= 0 {
		return false
	}
	name := token[:eq]
	for i := 0; i < len(name); i++ {
		c := name[i]
		if i == 0 {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_') {
				return false
			}
		} else {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
				return false
			}
		}
	}
	upper := strings.ToUpper(name)
	switch upper {
	case "PATH", "IFS", "ENV", "BASH_ENV", "LD_PRELOAD", "LD_LIBRARY_PATH",
		"PAGER", "GIT_PAGER", "GIT_EXEC_PATH", "GIT_EXTERNAL_DIFF",
		"GIT_SSH", "GIT_SSH_COMMAND", "GIT_EDITOR", "EDITOR", "VISUAL":
		return false
	}
	if strings.HasPrefix(upper, "DYLD_") || strings.HasPrefix(upper, "LD_") || strings.HasPrefix(upper, "GIT_CONFIG") {
		return false
	}
	return true
}

func isReadOnlySimpleCommand(args []string) bool {
	for len(args) > 0 && isSafeReadOnlyEnvAssignment(args[0]) {
		args = args[1:]
	}
	if len(args) == 0 {
		return false
	}

	for {
		name, ok := resolveReadOnlyCommandName(args[0])
		if !ok {
			return false
		}
		if name == "command" {
			rest := args[1:]
			for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
				if rest[0] == "--" {
					rest = rest[1:]
					break
				}
				if rest[0] == "-v" || rest[0] == "-V" {
					return len(rest) >= 2
				}
				if rest[0] == "-p" {
					rest = rest[1:]
					continue
				}
				return false
			}
			if len(rest) == 0 {
				return false
			}
			args = rest
			continue
		}
		if name == "env" {
			rest := args[1:]
			for len(rest) > 0 {
				if rest[0] == "--" {
					rest = rest[1:]
					break
				}
				if rest[0] == "-i" || rest[0] == "-" || rest[0] == "-0" || rest[0] == "--null" {
					rest = rest[1:]
					continue
				}
				if rest[0] == "-u" && len(rest) >= 2 {
					rest = rest[2:]
					continue
				}
				if strings.HasPrefix(rest[0], "--unset=") {
					rest = rest[1:]
					continue
				}
				if isSafeReadOnlyEnvAssignment(rest[0]) {
					rest = rest[1:]
					continue
				}
				if strings.HasPrefix(rest[0], "-") {
					return false
				}
				break
			}
			if len(rest) == 0 {
				return true
			}
			args = rest
			continue
		}

		switch name {
		case "grep", "egrep", "fgrep", "ag", "ack",
			"ls", "dir", "stat", "file", "wc", "du", "df", "pwd",
			"realpath", "readlink", "dirname", "basename", "which", "type",
			"uname", "id", "whoami", "hostname", "date", "printenv",
			"cat", "head", "tail", "nl", "tac", "rev", "cut", "tr",
			"comm", "diff", "cmp", "jq", "column", "fold", "fmt", "strings",
			"hexdump", "od", "md5sum", "sha1sum", "sha256sum", "sha512sum",
			"cksum", "echo", "test", "[", "true", "false":
			return true
		case "uniq":
			return isReadOnlyUniq(args[1:])
		case "xxd":
			return isReadOnlyXXD(args[1:])
		case "tree":
			return isReadOnlyTree(args[1:])
		case "printf":
			return isReadOnlyPrintf(args[1:])
		case "rg":
			return isReadOnlyRG(args[1:])
		case "sort":
			return isReadOnlySort(args[1:])
		case "find":
			return isReadOnlyFind(args[1:])
		case "fd", "fdfind":
			return isReadOnlyFD(args[1:])
		case "sed":
			return isReadOnlySed(args[1:])
		case "git":
			return isReadOnlyGit(args[1:])
		default:
			return false
		}
	}
}

func resolveReadOnlyCommandName(cmd string) (string, bool) {
	if cmd == "" {
		return "", false
	}
	if !strings.Contains(cmd, "/") {
		return cmd, true
	}
	dir := filepath.Dir(cmd)
	if dir == "/bin" || dir == "/usr/bin" || dir == "/usr/local/bin" {
		return filepath.Base(cmd), true
	}
	return "", false
}

func isReadOnlyRG(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return true
		}
		if arg == "--pre" || strings.HasPrefix(arg, "--pre=") {
			return false
		}
	}
	return true
}

func isReadOnlySort(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return true
		}
		if arg == "-o" || arg == "--output" || strings.HasPrefix(arg, "--output=") {
			return false
		}
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.ContainsRune(arg[1:], 'o') {
			return false
		}
	}
	return true
}

func isReadOnlyFind(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fls", "-fprintf":
			return false
		}
	}
	return true
}

func isReadOnlyUniq(args []string) bool {
	positionals := 0
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals += len(args) - i - 1
			break
		}
		switch arg {
		case "-f", "-s", "-w", "--skip-fields", "--skip-chars", "--check-chars", "--group", "--all-repeated":
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			continue
		}
		positionals++
	}
	return positionals <= 1
}

func isReadOnlyXXD(args []string) bool {
	positionals := 0
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals += len(args) - i - 1
			break
		}
		if arg == "-r" || arg == "--revert" {
			return false
		}
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.ContainsRune(arg[1:], 'r') {
			return false
		}
		switch arg {
		case "-c", "-cols", "-g", "-groupsize", "-l", "-len", "-n", "-o", "-s", "-seek", "-R":
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			continue
		}
		positionals++
	}
	return positionals <= 1
}

func isReadOnlyTree(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return true
		}
		if arg == "-o" || arg == "--output" || strings.HasPrefix(arg, "--output=") {
			return false
		}
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.ContainsRune(arg[1:], 'o') {
			return false
		}
	}
	return true
}

func isReadOnlyPrintf(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return true
		}
		if strings.HasPrefix(arg, "-") {
			return false
		}
		return true
	}
	return true
}

func isReadOnlyFD(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return true
		}
		if arg == "-x" || arg == "-X" || arg == "--exec" || arg == "--exec-batch" ||
			strings.HasPrefix(arg, "--exec=") || strings.HasPrefix(arg, "--exec-batch=") {
			return false
		}
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.ContainsAny(arg[1:], "xX") {
			return false
		}
	}
	return true
}

func isReadOnlySed(args []string) bool {
	if len(args) < 2 {
		return false
	}
	quiet := false
	var scripts []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if arg == "-n" || arg == "--quiet" || arg == "--silent" {
			quiet = true
			continue
		}
		if arg == "-e" || arg == "--expression" {
			if i+1 >= len(args) {
				return false
			}
			i++
			scripts = append(scripts, args[i])
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return false
		}
		if len(scripts) == 0 {
			scripts = append(scripts, arg)
		}
	}
	if !quiet || len(scripts) == 0 {
		return false
	}
	for _, script := range scripts {
		if !isSedPrintRangeOnly(script) {
			return false
		}
	}
	return true
}

func isSedPrintRangeOnly(script string) bool {
	script = strings.TrimSpace(script)
	if script == "" {
		return false
	}
	for _, part := range strings.Split(script, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.HasSuffix(part, "p") {
			return false
		}
		addr := strings.TrimSpace(strings.TrimSuffix(part, "p"))
		for i := 0; i < len(addr); i++ {
			c := addr[i]
			if !((c >= '0' && c <= '9') || c == ',' || c == '$' || c == ' ') {
				return false
			}
		}
	}
	return true
}

func isReadOnlyGit(args []string) bool {
	i := 0
	for i < len(args) {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			break
		}
		switch arg {
		case "--no-pager", "-P", "--bare", "--no-optional-locks",
			"--literal-pathspecs", "--glob-pathspecs", "--noglob-pathspecs", "--icase-pathspecs":
			i++
		case "-C", "--git-dir", "--work-tree", "--namespace":
			if i+1 >= len(args) {
				return false
			}
			i += 2
		default:
			if strings.HasPrefix(arg, "--git-dir=") || strings.HasPrefix(arg, "--work-tree=") ||
				strings.HasPrefix(arg, "--namespace=") || strings.HasPrefix(arg, "-C") {
				i++
				continue
			}
			return false
		}
	}
	if i >= len(args) {
		return false
	}
	sub := args[i]
	rest := args[i+1:]

	for _, arg := range rest {
		if arg == "--" {
			break
		}
		if arg == "--output" || strings.HasPrefix(arg, "--output=") || arg == "--ext-diff" {
			return false
		}
	}

	switch sub {
	case "status", "diff", "log", "show", "blame", "annotate", "rev-parse", "rev-list",
		"describe", "shortlog", "ls-files", "ls-tree", "cat-file", "grep",
		"merge-base", "name-rev", "check-ignore", "diff-index", "diff-files",
		"diff-tree", "for-each-ref", "show-ref", "count-objects", "var", "version", "help":
		return true
	case "branch":
		listMode := false
		positionals := 0
		for j := 0; j < len(rest); j++ {
			a := rest[j]
			if a == "--" {
				positionals += len(rest) - j - 1
				break
			}
			switch a {
			case "-d", "-D", "-m", "-M", "-c", "-C", "-f", "-u",
				"--delete", "--move", "--copy", "--force", "--set-upstream-to",
				"--unset-upstream", "--edit-description", "--create-reflog":
				return false
			case "-l", "--list", "--show-current":
				listMode = true
			case "--contains", "--no-contains", "--merged", "--no-merged", "--points-at":
				listMode = true
				if j+1 < len(rest) && !strings.HasPrefix(rest[j+1], "-") {
					j++
				}
			default:
				if strings.HasPrefix(a, "--set-upstream-to=") {
					return false
				}
				if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a[1:], "dDmMcCfu") {
					return false
				}
				if strings.HasPrefix(a, "--contains=") || strings.HasPrefix(a, "--no-contains=") ||
					strings.HasPrefix(a, "--merged=") || strings.HasPrefix(a, "--no-merged=") ||
					strings.HasPrefix(a, "--points-at=") {
					listMode = true
					continue
				}
				if !strings.HasPrefix(a, "-") {
					positionals++
				}
			}
		}
		return positionals == 0 || listMode
	case "tag":
		listOrVerify := false
		positionals := 0
		for j := 0; j < len(rest); j++ {
			a := rest[j]
			if a == "--" {
				positionals += len(rest) - j - 1
				break
			}
			switch a {
			case "-a", "-s", "-u", "-d", "-f", "-m", "-F", "--annotate", "--sign", "--delete", "--force", "--message", "--file":
				return false
			case "-l", "--list", "-v", "--verify":
				listOrVerify = true
			default:
				if strings.HasPrefix(a, "--message=") || strings.HasPrefix(a, "--file=") || strings.HasPrefix(a, "--local-user=") {
					return false
				}
				if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a[1:], "asudfmF") {
					return false
				}
				if !strings.HasPrefix(a, "-") {
					positionals++
				}
			}
		}
		return positionals == 0 || listOrVerify
	case "remote":
		if len(rest) == 0 {
			return true
		}
		switch rest[0] {
		case "-v", "--verbose", "show", "get-url":
			return true
		default:
			return false
		}
	case "stash":
		return len(rest) > 0 && (rest[0] == "list" || rest[0] == "show")
	case "config":
		hasReadFlag := false
		for _, a := range rest {
			if a == "--" {
				break
			}
			switch a {
			case "--unset", "--unset-all", "--replace-all", "--add", "--edit", "-e", "--remove-section", "--rename-section":
				return false
			case "--get", "--get-all", "--get-regexp", "--list", "-l", "--show-origin", "--show-scope":
				hasReadFlag = true
			}
		}
		return hasReadFlag
	case "symbolic-ref":
		positionals := 0
		for j, a := range rest {
			if a == "--" {
				positionals += len(rest) - j - 1
				break
			}
			if a == "-d" || a == "--delete" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsRune(a[1:], 'd')) {
				return false
			}
			if !strings.HasPrefix(a, "-") {
				positionals++
			}
		}
		return positionals == 1
	default:
		return false
	}
}
