//go:build !linux && !darwin

package storage

import "os"

// FileChangeStamp requests a bounded content check when no reliable stamp exists.
func FileChangeStamp(os.FileInfo) (int64, bool) { return 0, false }
