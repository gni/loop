//go:build linux

package storage

import (
	"os"
	"syscall"
)

// FileChangeStamp tracks ctime even when a writer restores size and mtime.
func FileChangeStamp(info os.FileInfo) (int64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return stat.Ctim.Sec*1e9 + stat.Ctim.Nsec, true
}
