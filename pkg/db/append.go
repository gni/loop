package db

import (
	"errors"
	"fmt"
	"os"
)

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
