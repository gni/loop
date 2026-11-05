//go:build windows

package file

// Windows does not support syncing a directory opened with os.Open. The file
// itself is synced before replacement; directory durability is platform-defined.
func syncParentDirectory(string) error { return nil }
