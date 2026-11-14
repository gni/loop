package evidence

import (
	"regexp"
	"strings"
)

// A check describes an executed command or parser, never a model assessment.
type Check struct {
	Kind      string `json:"kind"`
	Target    string `json:"target,omitempty"`
	Command   string `json:"command,omitempty"`
	Status    string `json:"status"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	Detail    string `json:"detail,omitempty"`
	StartedAt int64  `json:"started_at,omitempty"`
}

func CloneChecks(checks []Check) []Check {
	result := append([]Check(nil), checks...)
	for i := range result {
		if result[i].ExitCode != nil {
			value := *result[i].ExitCode
			result[i].ExitCode = &value
		}
	}
	return result
}

// CommandCheck recognizes simple direct checks. Shell composition, substitutions
// and scripts with unknown semantics stay ordinary command evidence.
func CommandCheck(command string, code *int, outcome string) *Check {
	if strings.ContainsAny(command, "\n\r;&|<>`$\\\"'") {
		return nil
	}
	args := strings.Fields(command)
	if len(args) == 0 {
		return nil
	}
	for _, arg := range args[1:] {
		if arg == "-n" || arg == "--dry-run" || arg == "--collect-only" || arg == "--co" || arg == "--no-run" || arg == "--list" || arg == "-list" || strings.HasPrefix(arg, "-list=") || arg == "--help" || arg == "-h" || arg == "--version" {
			return nil
		}
	}
	kind := ""
	if len(args) > 1 {
		switch args[0] {
		case "go":
			switch args[1] {
			case "build":
				kind = "build"
			case "test":
				// Compiling a test binary does not execute its tests. Keep
				// commands carrying this flag as ordinary command evidence.
				for _, arg := range args[2:] {
					flag, _, _ := strings.Cut(arg, "=")
					if flag == "-c" || flag == "--c" {
						return nil
					}
				}
				kind = "test"
			case "vet":
				kind = "lint"
			}
		case "cargo":
			switch args[1] {
			case "build", "check":
				kind = "build"
			case "test":
				kind = "test"
			case "clippy":
				kind = "lint"
			}
		case "npm", "pnpm", "yarn", "bun":
			if args[1] == "test" {
				kind = "test"
			}
			if len(args) == 3 && args[1] == "run" {
				switch args[2] {
				case "build":
					kind = "build"
				case "lint":
					kind = "lint"
				case "test":
					kind = "test"
				}
			}
		case "python", "python3":
			if len(args) > 2 && args[1] == "-m" && args[2] == "pytest" {
				kind = "test"
			}
		}
	}
	if args[0] == "pytest" {
		kind = "test"
	}
	if kind == "" {
		return nil
	}
	status := Unknown
	if outcome == NotExecuted {
		status = NotExecuted
	} else if outcome == Failed {
		status = Failed
	}
	if code != nil {
		status = Failed
		if *code == 0 && outcome == Succeeded {
			status = Passed
		}
		if *code == 126 || *code == 127 {
			status = Unavailable
		}
	}
	return &Check{Kind: kind, Command: command, Status: status, ExitCode: code}
}

var observedTests = regexp.MustCompile(`\b[1-9][0-9]* (?:passed|tests? run)|Ran [1-9][0-9]* tests?`)

func (c *Check) ObserveOutput(output string) {
	if c == nil || c.Kind != "test" || c.Status != Passed {
		return
	}
	lower := strings.ToLower(output)
	if !strings.Contains(lower, "[no test files]") && !strings.Contains(lower, "[no tests to run]") && !strings.Contains(lower, "no tests ran") && !strings.Contains(lower, "ran 0 tests") {
		return
	}
	if observedTests.MatchString(output) {
		return
	}
	for line := range strings.SplitSeq(output, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "ok") && !strings.Contains(line, "[no tests to run]") {
			return
		}
		if strings.Contains(line, `"Action":"run"`) && strings.Contains(line, `"Test":`) {
			return
		}
	}
	c.Status = Skipped
	c.Detail = "command succeeded but reported no tests executed"
}
