//go:build darwin

package storage

import (
	"os"
	"syscall"
)

// FileChangeStamp detects changes independently of the last write timestamp.
func FileChangeStamp(info os.FileInfo) (int64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return stat.Ctimespec.Sec*1e9 + stat.Ctimespec.Nsec, true
}
