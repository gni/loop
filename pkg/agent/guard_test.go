package agent

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// The guard only blocks repetition that happens back-to-back. Scattered calls
// across a turn or session are never penalised.

func TestGuard_PerFileReadLimitIsConsecutive(t *testing.T) {
	guard := NewTurnExecutionGuard()
	file := "src/config/settings.py"
	args := `{"path":"` + file + `"}`

	// Interleaving a different call breaks the streak, so 30 scattered reads of
	// the same file must never be blocked.
	for i := 1; i <= 30; i++ {
		if err := guard.CheckPreExecution("read", args); err != nil {
			t.Fatalf("scattered read %d unexpectedly blocked: %v", i, err)
		}
		guard.RecordPostExecution("read", args, "content of settings.py", nil)
		other := `{"pattern":"term_` + fmt.Sprint(i) + `"}`
		if err := guard.CheckPreExecution("grep", other); err != nil {
			t.Fatalf("interleaved grep %d unexpectedly blocked: %v", i, err)
		}
		guard.RecordPostExecution("grep", other, "matches", nil)
	}

	// 10 identical reads in a row are still allowed; the 11th is blocked.
	for i := 1; i <= ConsecutiveLimit; i++ {
		if err := guard.CheckPreExecution("read", args); err != nil {
			t.Fatalf("consecutive read %d unexpectedly blocked: %v", i, err)
		}
		guard.RecordPostExecution("read", args, "content of settings.py", nil)
	}

	err := guard.CheckPreExecution("read", args)
	if err == nil {
		t.Fatalf("expected read #%d of %s to be blocked, but it was allowed", ConsecutiveLimit+1, file)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("inspected %d times in a row", ConsecutiveLimit)) {
		t.Fatalf("unexpected error message: %v", err)
	}

	// Any different call resets the streak: the same file is readable again.
	if err := guard.CheckPreExecution("grep", `{"pattern":"settings"}`); err != nil {
		t.Fatalf("interleaved call unexpectedly blocked: %v", err)
	}
	guard.RecordPostExecution("grep", `{"pattern":"settings"}`, "matches", nil)
	if err := guard.CheckPreExecution("read", args); err != nil {
		t.Fatalf("read after breaking the streak was unexpectedly blocked: %v", err)
	}
}

func TestGuard_DistinctFileInspectionsAllowed(t *testing.T) {
	guard := NewTurnExecutionGuard()

	// Inspecting 20 distinct files and directories across the workspace should NEVER be blocked
	for i := 1; i <= 20; i++ {
		args := `{"path":"src/file_` + string(rune('a'+i)) + `.go"}`
		if err := guard.CheckPreExecution("read", args); err != nil {
			t.Fatalf("distinct file inspection %d unexpectedly blocked: %v", i, err)
		}
		guard.RecordPostExecution("read", args, "content", nil)
	}

	// Listing distinct directories should also be allowed
	dirs := []string{"src/core", "src/modules", "src/core/agents", "src/core/crypto", "src/core/errors", "src/core/types"}
	for _, d := range dirs {
		args := `{"path":"` + d + `"}`
		if err := guard.CheckPreExecution("list", args); err != nil {
			t.Fatalf("distinct directory list '%s' unexpectedly blocked: %v", d, err)
		}
		guard.RecordPostExecution("list", args, "entries", nil)
	}
}

func TestGuard_DirectoryInspectionLimit(t *testing.T) {
	guard := NewTurnExecutionGuard()
	dir := "src/core/errors"
	args := `{"path":"` + dir + `"}`

	for i := 1; i <= ConsecutiveLimit; i++ {
		if err := guard.CheckPreExecution("list", args); err != nil {
			t.Fatalf("list %d unexpectedly blocked: %v", i, err)
		}
		guard.RecordPostExecution("list", args, "entries", nil)
	}

	// The (ConsecutiveLimit+1)th consecutive list of the same directory is blocked
	err := guard.CheckPreExecution("list", args)
	if err == nil {
		t.Fatalf("expected consecutive list #%d of %s to be blocked", ConsecutiveLimit+1, dir)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("inspected %d times in a row", ConsecutiveLimit)) {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestGuard_BashFileInspectionExtraction(t *testing.T) {
	guard := NewTurnExecutionGuard()
	file := "src/config/settings.py"

	// read via tool 'read', then via bash 'cat', then via python pathlib: same
	// target, so they form one consecutive streak regardless of tool.
	readArgs := `{"path":"` + file + `"}`
	catArgs := `{"command":"cat src/config/settings.py"}`
	pyArgs := `{"command":"python3 -c \"import pathlib; print(pathlib.Path('src/config/settings.py').read_text())\""}`

	for _, step := range []struct{ tool, args string }{{"read", readArgs}, {"bash", catArgs}, {"bash", pyArgs}} {
		if err := guard.CheckPreExecution(step.tool, step.args); err != nil {
			t.Fatalf("%s inspection unexpectedly blocked: %v", step.tool, err)
		}
		guard.RecordPostExecution(step.tool, step.args, "content", nil)
	}

	if count := guard.FileReadCount(file); count != 3 {
		t.Fatalf("expected consecutive file read streak 3, got %d", count)
	}

	// Fill the streak to the limit, then the next inspection of the same target is blocked
	for i := 4; i <= ConsecutiveLimit; i++ {
		if err := guard.CheckPreExecution("bash", catArgs); err != nil {
			t.Fatalf("bash inspection %d unexpectedly blocked: %v", i, err)
		}
		guard.RecordPostExecution("bash", catArgs, "content", nil)
	}

	err := guard.CheckPreExecution("bash", catArgs)
	if err == nil {
		t.Fatalf("expected consecutive inspection #%d via bash cat to be blocked", ConsecutiveLimit+1)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("inspected %d times in a row", ConsecutiveLimit)) {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestGuard_ModificationResetsCounters(t *testing.T) {
	guard := NewTurnExecutionGuard()
	file := "src/config/settings.py"
	args := `{"path":"` + file + `"}`

	// Read ConsecutiveLimit times in a row
	for i := 0; i < ConsecutiveLimit; i++ {
		_ = guard.CheckPreExecution("read", args)
		guard.RecordPostExecution("read", args, "content", nil)
	}

	// Next read is blocked
	if err := guard.CheckPreExecution("read", args); err == nil {
		t.Fatalf("expected blocked read before modification")
	}

	// Apply an edit
	editArgs := `{"path":"` + file + `","oldText":"foo","newText":"bar"}`
	guard.RecordPostExecution("edit", editArgs, "successfully edited", nil)

	if guard.FileReadCount(file) != 0 {
		t.Fatalf("expected file read streak reset to 0, got %d", guard.FileReadCount(file))
	}

	// Now reading the file again is allowed
	if err := guard.CheckPreExecution("read", args); err != nil {
		t.Fatalf("reading file after edit was unexpectedly blocked: %v", err)
	}
}

func TestGuard_RepeatedFailingBashCommand(t *testing.T) {
	guard := NewTurnExecutionGuard()
	failingCmd := `{"command":"python3 -c \"import paththlib\""}`

	for i := 1; i <= ConsecutiveLimit; i++ {
		if err := guard.CheckPreExecution("bash", failingCmd); err != nil {
			t.Fatalf("attempt %d blocked: %v", i, err)
		}
		guard.RecordPostExecution("bash", failingCmd, "NameError", errors.New("command failed: exit status 1"))
	}

	// The (ConsecutiveLimit+1)th identical failing command is blocked
	err := guard.CheckPreExecution("bash", failingCmd)
	if err == nil {
		t.Fatalf("expected consecutively failing command to be blocked")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("failed %d times in a row", ConsecutiveLimit)) {
		t.Fatalf("unexpected error message: %v", err)
	}

	// Different command should NOT be blocked, and it resets the streak
	differentCmd := `{"command":"python3 -c \"import pathlib\""}`
	if err := guard.CheckPreExecution("bash", differentCmd); err != nil {
		t.Fatalf("different command unexpectedly blocked: %v", err)
	}
	guard.RecordPostExecution("bash", differentCmd, "ok", nil)
	if err := guard.CheckPreExecution("bash", failingCmd); err != nil {
		t.Fatalf("failing command unexpectedly still blocked after a different call: %v", err)
	}
}

func TestGuard_SuccessClearsFailureStreak(t *testing.T) {
	guard := NewTurnExecutionGuard()
	flakyCmd := `{"command":"pytest tests/ --flakey"}`

	for i := 1; i <= 5; i++ {
		if err := guard.CheckPreExecution("bash", flakyCmd); err != nil {
			t.Fatalf("attempt %d blocked: %v", i, err)
		}
		guard.RecordPostExecution("bash", flakyCmd, "NameError", errors.New("command failed: exit status 1"))
	}

	// A success clears the failure streak even though the call is identical.
	guard.RecordPostExecution("bash", flakyCmd, "tests passed", nil)
	if err := guard.CheckPreExecution("bash", flakyCmd); err != nil {
		t.Fatalf("command unexpectedly blocked after it succeeded: %v", err)
	}
}

func TestGuard_ConsecutiveIdenticalCalls(t *testing.T) {
	guard := NewTurnExecutionGuard()
	cmd := `{"command":"pytest tests/"}`

	for i := 1; i <= ConsecutiveLimit; i++ {
		if err := guard.CheckPreExecution("bash", cmd); err != nil {
			t.Fatalf("attempt %d unexpectedly blocked: %v", i, err)
		}
		// Succeeded (err == nil) but identical
		guard.RecordPostExecution("bash", cmd, "tests passed", nil)
	}

	// The (ConsecutiveLimit+1)th identical execution in a row is blocked
	err := guard.CheckPreExecution("bash", cmd)
	if err == nil {
		t.Fatalf("expected %dth consecutive identical call to be blocked", ConsecutiveLimit+1)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("identical tool call repeated %d times in a row", ConsecutiveLimit)) {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestGuard_ScatteredIdenticalCallsNeverBlocked(t *testing.T) {
	guard := NewTurnExecutionGuard()
	cmd := `{"command":"pytest tests/"}`

	// 40 pytest calls spread across the turn with other work in between must
	// never trip the guard.
	for i := 1; i <= 40; i++ {
		if err := guard.CheckPreExecution("bash", cmd); err != nil {
			t.Fatalf("scattered identical call %d unexpectedly blocked: %v", i, err)
		}
		guard.RecordPostExecution("bash", cmd, "tests passed", nil)

		other := `{"pattern":"x_` + fmt.Sprint(i) + `"}`
		guard.RecordPostExecution("grep", other, "matches", nil)
	}
}

// A read of the same file with a different offset/limit is a genuinely new
// inspection, so it must never inflate the repetition streak.
func TestGuard_SameFileDifferentWindowIsNotRepetition(t *testing.T) {
	guard := NewTurnExecutionGuard()
	file := "pkg/agent/guard.go"

	for i := 1; i <= 30; i++ {
		args := fmt.Sprintf(`{"path":"%s","offset":%d,"limit":20}`, file, i*20)
		if err := guard.CheckPreExecution("read", args); err != nil {
			t.Fatalf("window read %d unexpectedly blocked: %v", i, err)
		}
		guard.RecordPostExecution("read", args, "content", nil)
	}

	// Re-reading an already observed window back-to-back still counts as repetition.
	args := `{"path":"` + file + `","offset":600,"limit":20}`
	for i := 1; i <= 11; i++ {
		err := guard.CheckPreExecution("read", args)
		if err == nil {
			guard.RecordPostExecution("read", args, "content", nil)
			continue
		}
		if i < guard.reminderThresholds[0] {
			t.Fatalf("window read %d unexpectedly blocked early: %v", i, err)
		}
		if !strings.Contains(err.Error(), "loop detected") {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	t.Fatal("repeated identical window never triggered the guard")
}

// Editing a file invalidates prior observations, so the window cache must be cleared.
func TestGuard_ModificationClearsReadWindows(t *testing.T) {
	guard := NewTurnExecutionGuard()
	file := "pkg/agent/guard.go"
	args := `{"path":"` + file + `","offset":10,"limit":20}`

	guard.CheckPreExecution("read", args)
	guard.RecordPostExecution("read", args, "content", nil)
	guard.RecordPostExecution("edit", `{"path":"`+file+`","updates":[]}`, "ok", nil)

	if isReadWindowNew("read", args, guard.readWindows) != true {
		t.Fatal("read window should be fresh after the file was modified")
	}
}

// fakeInvalidator records the absolute paths the guard forgets.
type fakeInvalidator struct {
	forgotten []string
}

func (f *fakeInvalidator) Forget(absPath string) {
	f.forgotten = append(f.forgotten, absPath)
}

// A successful mutating bash command must invalidate the tracker's observation for the
// affected file, so the next edit cannot rely on a stale read.
func TestGuard_BashMutationInvalidatesObservation(t *testing.T) {
	guard := NewTurnExecutionGuard()
	inv := &fakeInvalidator{}
	guard.SetObservationInvalidator(inv)
	guard.SetPathResolver(func(p string) string { return "/workspace/" + p })

	file := "src/config/settings.py"
	readArgs := `{"path":"` + file + `"}`
	guard.CheckPreExecution("read", readArgs)
	guard.RecordPostExecution("read", readArgs, "content", nil)

	bashArgs := `{"command":"sed -i 's/a/b/' src/config/settings.py"}`
	if err := guard.CheckPreExecution("bash", bashArgs); err != nil {
		t.Fatalf("mutating bash unexpectedly blocked: %v", err)
	}
	guard.RecordPostExecution("bash", bashArgs, "ok", nil)

	if len(inv.forgotten) != 1 || inv.forgotten[0] != "/workspace/src/config/settings.py" {
		t.Fatalf("expected observation forgotten at resolved path, got %v", inv.forgotten)
	}
}

// A failed mutating bash command must NOT invalidate the observation, since the file was
// not actually changed on disk.
func TestGuard_FailedBashMutationKeepsObservation(t *testing.T) {
	guard := NewTurnExecutionGuard()
	inv := &fakeInvalidator{}
	guard.SetObservationInvalidator(inv)

	bashArgs := `{"command":"sed -i 's/a/b/' src/config/settings.py"}`
	guard.CheckPreExecution("bash", bashArgs)
	guard.RecordPostExecution("bash", bashArgs, "", errors.New("sed: no such file"))

	if len(inv.forgotten) != 0 {
		t.Fatalf("failed mutation must not forget the observation, got %v", inv.forgotten)
	}
}

// Non-mutating bash (a plain read via cat) must not invalidate the observation.
func TestGuard_NonMutatingBashKeepsObservation(t *testing.T) {
	guard := NewTurnExecutionGuard()
	inv := &fakeInvalidator{}
	guard.SetObservationInvalidator(inv)

	catArgs := `{"command":"cat src/config/settings.py"}`
	guard.CheckPreExecution("bash", catArgs)
	guard.RecordPostExecution("bash", catArgs, "content", nil)

	if len(inv.forgotten) != 0 {
		t.Fatalf("read-only bash must not forget the observation, got %v", inv.forgotten)
	}
}

// The per-target read-window cache must be bounded so a pathological session that inspects
// many distinct slices of one file cannot grow it without limit.
func TestGuard_ReadWindowsAreBounded(t *testing.T) {
	guard := NewTurnExecutionGuard()
	file := "pkg/agent/guard.go"

	for i := 0; i < maxWindowsPerTarget+20; i++ {
		args := fmt.Sprintf(`{"path":"%s","offset":%d,"limit":%d}`, file, i, i+1)
		guard.CheckPreExecution("read", args)
		guard.RecordPostExecution("read", args, "content", nil)
	}

	if got := len(guard.readWindows[filepath.Clean(file)]); got > maxWindowsPerTarget {
		t.Fatalf("read window cache for target exceeded bound: %d > %d", got, maxWindowsPerTarget)
	}
}
