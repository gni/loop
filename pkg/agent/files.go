package agent

import (
	"context"
	"os"
)

func (a *Agent) ensureGuided() *GuidedController {
	if a == nil {
		return nil
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.Guided == nil {
		enabled := a.Config != nil && a.Config.GuidedMode
		a.Guided = NewGuidedController(enabled)
	}
	return a.Guided
}

func (a *Agent) IsGuidedMode() bool {
	if a == nil {
		return false
	}
	if a.Guided != nil {
		return a.Guided.IsEnabled()
	}
	return a.Config != nil && a.Config.GuidedMode
}

func (a *Agent) SetGuidedMode(enabled bool) {
	_ = a.UpdateLiveSettings(func(next *LiveSettings) { next.GuidedMode = enabled })
}

func (a *Agent) RollbackChanges() ([]string, error) {
	if a == nil {
		return nil, nil
	}
	paths, err := a.ensureGuided().rollbackContext(a.Context(), a.FileObservations, a.SafePath)
	for _, path := range paths {
		a.InvalidateCodeIndex(path)
	}
	if len(paths) > 0 {
		a.ReloadSkills()
	}
	return paths, err
}

func (a *Agent) BeginFileMutation(ctx context.Context, path string, data []byte, mode os.FileMode) (func(), error) {
	return a.ensureGuided().beginMutation(ctx, path, data, mode, func() error {
		if a.FileObservations != nil {
			return a.FileObservations.CheckMutationAllowed(path, false)
		}
		return nil
	})
}

func (a *Agent) HasSnapshot(absPath string) bool {
	if a == nil {
		return false
	}
	return a.ensureGuided().HasSnapshot(absPath)
}

func (a *Agent) DiscardSnapshot(absPath string) {
	if a == nil {
		return
	}
	a.ensureGuided().DiscardSnapshot(absPath)
}

func (a *Agent) RecordRead(absPath string, data []byte) {
	if a != nil && a.FileObservations != nil {
		a.FileObservations.RecordRead(absPath, data)
	}
}

func (a *Agent) CheckMutationAllowed(absPath string, isEdit bool) error {
	if a == nil {
		return nil
	}
	if a.FileObservations != nil {
		if err := a.FileObservations.CheckMutationAllowed(absPath, isEdit); err != nil {
			return err
		}
	}
	return a.ensureGuided().captureFileSnapshot(absPath, int64(a.GetLimits().MaxReadFileBytes))
}

// InvalidateCodeIndex refreshes declarations after committed native edits.
func (a *Agent) InvalidateCodeIndex(path string) {
	if a.Registry == nil {
		return
	}
	if executor, exists := a.Registry.Lookup("search_symbols"); exists {
		if index, ok := executor.(interface{ Invalidate(string) }); ok {
			index.Invalidate(path)
		}
	}
}

func (a *Agent) RecordMutation(absPath string, data []byte) {
	if a == nil {
		return
	}
	if a.FileObservations != nil {
		a.FileObservations.RecordMutation(absPath, data)
	}
}
