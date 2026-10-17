package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"loop/pkg/agent/tool"
	filetool "loop/pkg/agent/tool/file"
)

type GuidedPhase string

const (
	GuidedPhaseExplore GuidedPhase = "explore"
	GuidedPhasePlan    GuidedPhase = "plan"
	GuidedPhaseExecute GuidedPhase = "execute"
)

type FileSnapshot struct {
	AbsPath string
	Existed bool
	Data    []byte
	Mode    os.FileMode
}

type GuidedController struct {
	mu           sync.RWMutex
	enabled      bool
	phase        GuidedPhase
	planApproved bool
	planHash     string
	snapshots    map[string]FileSnapshot
}

func NewGuidedController(enabled bool) *GuidedController {
	return &GuidedController{
		enabled:   enabled,
		phase:     GuidedPhaseExplore,
		snapshots: make(map[string]FileSnapshot),
	}
}

func (gc *GuidedController) SetEnabled(enabled bool) {
	if gc == nil {
		return
	}
	gc.mu.Lock()
	defer gc.mu.Unlock()
	if enabled && !gc.enabled {
		gc.phase = GuidedPhaseExplore
		gc.planApproved = false
		gc.planHash = ""
	}
	gc.enabled = enabled
}

func (gc *GuidedController) IsEnabled() bool {
	if gc == nil {
		return false
	}
	gc.mu.RLock()
	defer gc.mu.RUnlock()
	return gc.enabled
}

func (gc *GuidedController) Phase() GuidedPhase {
	if gc == nil {
		return GuidedPhaseExecute
	}
	gc.mu.RLock()
	defer gc.mu.RUnlock()
	if !gc.enabled {
		return GuidedPhaseExecute
	}
	return gc.phase
}

func (gc *GuidedController) IsPlanApproved() bool {
	if gc == nil {
		return false
	}
	gc.mu.RLock()
	defer gc.mu.RUnlock()
	return gc.planApproved
}

func (gc *GuidedController) ResetForNewUserTurn(todos []tool.TodoItem) {
	if gc == nil {
		return
	}
	gc.mu.Lock()
	defer gc.mu.Unlock()
	if !gc.enabled {
		return
	}
	allDone := true
	for _, t := range todos {
		if !strings.EqualFold(strings.TrimSpace(t.Status), "completed") {
			allDone = false
			break
		}
	}
	if len(todos) == 0 || allDone {
		gc.phase = GuidedPhaseExplore
		gc.planApproved = false
		gc.planHash = ""
	}
}

func (gc *GuidedController) CheckToolAllowed(a *Agent, toolName string) (bool, error) {
	if gc == nil || !gc.IsEnabled() {
		return false, nil
	}
	norm := tool.NormalizeName(strings.ToLower(strings.TrimSpace(toolName)))
	if IsInspectionTool(norm) {
		return false, nil
	}

	var todos []tool.TodoItem
	if a != nil {
		todos = a.GetTodos()
	}
	if len(todos) == 0 {
		gc.mu.Lock()
		gc.phase = GuidedPhaseExplore
		gc.mu.Unlock()
		return false, fmt.Errorf("guided mode: inspect files with read/grep/find/list and register a plan with 'todo' before calling '%s'", toolName)
	}

	currentHash := hashTodos(todos)
	gc.mu.Lock()
	defer gc.mu.Unlock()
	if !gc.planApproved || gc.planHash != currentHash {
		gc.phase = GuidedPhasePlan
		return true, nil
	}
	gc.phase = GuidedPhaseExecute
	return false, nil
}

func (gc *GuidedController) ApprovePlan(todos []tool.TodoItem) {
	if gc == nil {
		return
	}
	gc.mu.Lock()
	defer gc.mu.Unlock()
	gc.planApproved = true
	gc.planHash = hashTodos(todos)
	gc.phase = GuidedPhaseExecute
}

func FormatPlanSummary(todos []tool.TodoItem) string {
	var sb strings.Builder
	sb.WriteString("Proposed execution plan:\n")
	for i, item := range todos {
		status := item.Status
		if status == "" {
			status = "pending"
		}
		sb.WriteString(fmt.Sprintf("  %d. [%s] %s\n", i+1, status, item.Task))
	}
	sb.WriteString("\nApprove this plan to begin execution?")
	return sb.String()
}

func normalizeSnapshotPath(absPath string) string {
	if abs, err := filepath.Abs(absPath); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(absPath)
}

func (gc *GuidedController) HasSnapshot(absPath string) bool {
	if gc == nil || absPath == "" {
		return false
	}
	clean := normalizeSnapshotPath(absPath)
	gc.mu.RLock()
	defer gc.mu.RUnlock()
	_, exists := gc.snapshots[clean]
	return exists
}

func (gc *GuidedController) CaptureFileSnapshot(absPath string) {
	if gc == nil || absPath == "" {
		return
	}
	clean := normalizeSnapshotPath(absPath)
	gc.mu.Lock()
	defer gc.mu.Unlock()
	if gc.snapshots == nil {
		gc.snapshots = make(map[string]FileSnapshot)
	}
	if _, exists := gc.snapshots[clean]; exists {
		return
	}

	mode := os.FileMode(0644)
	existed := false
	var data []byte

	if info, err := os.Stat(clean); err == nil && !info.IsDir() {
		existed = true
		mode = info.Mode().Perm()
		if mode == 0 {
			mode = 0644
		}
		if raw, readErr := os.ReadFile(clean); readErr == nil {
			data = append([]byte(nil), raw...)
		}
	}

	gc.snapshots[clean] = FileSnapshot{
		AbsPath: clean,
		Existed: existed,
		Data:    data,
		Mode:    mode,
	}
}

func (gc *GuidedController) DiscardSnapshot(absPath string) {
	if gc == nil || absPath == "" {
		return
	}
	clean := normalizeSnapshotPath(absPath)
	gc.mu.Lock()
	defer gc.mu.Unlock()
	delete(gc.snapshots, clean)
}

func (gc *GuidedController) ClearSnapshots() {
	if gc == nil {
		return
	}
	gc.mu.Lock()
	defer gc.mu.Unlock()
	gc.snapshots = make(map[string]FileSnapshot)
}

func (gc *GuidedController) Snapshots() []FileSnapshot {
	if gc == nil {
		return nil
	}
	gc.mu.RLock()
	defer gc.mu.RUnlock()
	out := make([]FileSnapshot, 0, len(gc.snapshots))
	for _, s := range gc.snapshots {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].AbsPath < out[j].AbsPath
	})
	return out
}

func (gc *GuidedController) Rollback(obs *tool.FileObservationTracker) ([]string, error) {
	if gc == nil {
		return nil, nil
	}
	gc.mu.Lock()
	defer gc.mu.Unlock()
	if len(gc.snapshots) == 0 {
		return nil, nil
	}

	var restored []string
	var firstErr error
	remaining := make(map[string]FileSnapshot)

	for pathKey, snap := range gc.snapshots {
		if !snap.Existed {
			if err := os.Remove(snap.AbsPath); err != nil && !os.IsNotExist(err) {
				if firstErr == nil {
					firstErr = err
				}
				remaining[pathKey] = snap
				continue
			}
			if obs != nil {
				obs.Forget(snap.AbsPath)
			}
			restored = append(restored, fmt.Sprintf("removed %s", snap.AbsPath))
		} else {
			mode := snap.Mode
			if mode == 0 {
				mode = 0644
			}
			if err := filetool.AtomicWriteFile(snap.AbsPath, snap.Data, mode); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				remaining[pathKey] = snap
				continue
			}
			if obs != nil {
				obs.RecordMutation(snap.AbsPath, snap.Data)
			}
			restored = append(restored, fmt.Sprintf("restored %s", snap.AbsPath))
		}
	}
	sort.Strings(restored)
	gc.snapshots = remaining
	return restored, firstErr
}

func PreviewToolDiff(a *Agent, toolName, arguments string) string {
	if a == nil {
		return ""
	}
	norm := tool.NormalizeName(toolName)
	if norm != "write" && norm != "edit" {
		return ""
	}
	targetPath := filetool.ExtractFilePath(arguments)
	if targetPath == "" {
		return ""
	}
	safePath, err := a.SafePath(targetPath)
	if err != nil {
		return ""
	}

	existingBytes, readErr := os.ReadFile(safePath)
	oldContent := ""
	if readErr == nil {
		_, rawStr := filetool.SplitBOM(existingBytes)
		oldContent = strings.ReplaceAll(rawStr, "\r\n", "\n")
	}

	if norm == "write" {
		newContent, ok := filetool.ExtractFileContent(arguments)
		if !ok {
			return ""
		}
		return filetool.GenerateDisplayDiff(oldContent, strings.ReplaceAll(newContent, "\r\n", "\n"), 3)
	}

	var args struct {
		Updates      []filetool.ReplaceEdit `json:"updates"`
		Edits        []filetool.ReplaceEdit `json:"edits"`
		Replacements []filetool.ReplaceEdit `json:"replacements"`
	}
	_ = json.Unmarshal([]byte(arguments), &args)
	edits := filetool.ExtractEdits(arguments, args.Updates, args.Edits, args.Replacements)
	if len(edits) == 0 || readErr != nil {
		return ""
	}

	previewContent, changed := filetool.ApplyEditsPreview(oldContent, edits)
	if !changed {
		return ""
	}
	return filetool.GenerateDisplayDiff(oldContent, previewContent, 3)
}

func hashTodos(todos []tool.TodoItem) string {
	var sb strings.Builder
	for _, t := range todos {
		sb.WriteString(t.ID + ":" + t.Task + "|")
	}
	return sb.String()
}