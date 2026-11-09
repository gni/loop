package storage

import (
	"fmt"
	"io"
	"math"
)

func ReadFile(path string, limit int64) ([]byte, error) {
	if limit < 0 {
		return nil, fmt.Errorf("file size limit must not be negative")
	}
	file, err := OpenRegularFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, limit)
	}
	readLimit := limit
	if readLimit < math.MaxInt64 {
		readLimit++
	}
	data, err := io.ReadAll(io.LimitReader(file, readLimit))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, limit)
	}
	return data, nil
}
