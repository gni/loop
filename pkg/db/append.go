package db

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// JSONL permits a complete final record without a newline. Separate it from
// the next record under the same rollback boundary as the append itself.
func ensureSessionAppendBoundary(file *os.File, size int64) error {
	if size == 0 {
		return nil
	}
	var last [1]byte
	if _, err := file.ReadAt(last[:], size-1); err != nil {
		return fmt.Errorf("read session append boundary: %w", err)
	}
	if last[0] != '\n' {
		n, err := file.Write([]byte{'\n'})
		if err == nil && n != 1 {
			err = io.ErrShortWrite
		}
		if err != nil {
			return fmt.Errorf("separate final session record: %w", err)
		}
	}
	return nil
}

// A failed append may have written half a JSON record. Restore the previous
// boundary under the store lock so older conversation data remains readable.
func rollbackSessionAppend(file *os.File, size int64, created bool, cause error) error {
	rollbackErr := file.Truncate(size)
	if rollbackErr == nil {
		rollbackErr = file.Sync()
	}
	if created {
		rollbackErr = errors.Join(rollbackErr, file.Close(), os.Remove(file.Name()))
	}
	if rollbackErr != nil {
		return errors.Join(cause, fmt.Errorf("restore session after failed append: %w", rollbackErr))
	}
	return cause
}
