package ui

import (
	"os"
	"sync"
	"time"

	"loop/pkg/resources"
)

var (
	gitBranchCacheMu   sync.Mutex
	gitBranchCache     string
	gitBranchWorkspace string
	gitBranchExpiry    time.Time
)

// CachedGitBranch returns the current repository git branch with a 2-second cache.
func CachedGitBranch() string {
	gitBranchCacheMu.Lock()
	defer gitBranchCacheMu.Unlock()
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if cwd == gitBranchWorkspace && time.Now().Before(gitBranchExpiry) {
		return gitBranchCache
	}
	gitBranchWorkspace = cwd
	gitBranchCache = DetectGitBranch(cwd)
	gitBranchExpiry = time.Now().Add(2 * time.Second)
	return gitBranchCache
}

// DetectGitBranch returns the branch or detached HEAD for dir's repository.
func DetectGitBranch(dir string) string {
	return resources.DetectGitBranch(dir)
}
