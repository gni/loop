//go:build windows

package storage

// Windows does not expose portable directory fsync through os.File. Files are
// individually flushed before replacement; recovery remains available after
// process interruption, without claiming Unix-style metadata flush guarantees.
func SyncDirectory(string) error { return nil }
