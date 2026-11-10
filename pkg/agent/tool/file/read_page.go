package file

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"loop/pkg/storage"
)

// Spill files can exceed the ordinary read limit. Scan bounded chunks and
// retain only the requested page, including a bounded prefix of each line.
func readSpillPage(ctx context.Context, workspace, path, header string, offset, limit, budget, lineLimit int) (string, error) {
	file, err := storage.OpenSpill(ctx, workspace, path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() > storage.MaxSpillBytes {
		return "", fmt.Errorf("spill file exceeds %d MiB", storage.MaxSpillBytes>>20)
	}
	reader := bufio.NewReaderSize(io.LimitReader(file, storage.MaxSpillBytes+1), 64<<10)
	var lines []string
	var prefix []byte
	totalBytes, line, pageBytes := 0, 1, 0
	keepBytes := lineLimit*utf8.UTFMax + utf8.UTFMax
	clipped, pageFull := false, false
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		fragment, err := reader.ReadSlice('\n')
		totalBytes += len(fragment)
		if totalBytes > storage.MaxSpillBytes {
			return "", fmt.Errorf("spill file exceeds %d MiB", storage.MaxSpillBytes>>20)
		}
		if bytes.IndexByte(fragment, 0) >= 0 {
			return "", fmt.Errorf("cannot read binary spill as text")
		}
		keep := !pageFull && line >= offset && line-offset < limit
		if keep && !clipped {
			for _, ch := range fragment {
				if ch == '\r' || ch == '\n' {
					continue
				}
				if len(prefix) == keepBytes {
					clipped = true
					break
				}
				prefix = append(prefix, ch)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil && err != io.EOF {
			return "", err
		}
		if keep {
			text := SanitizeUTF8(prefix)
			short := TruncateLine(text, lineLimit)
			if clipped && short == text {
				short += LineTruncatedMarker()
			}
			lines = append(lines, short)
			pageBytes += len(short) + 1
			pageFull = pageBytes >= budget
		}
		if err == io.EOF {
			break
		}
		prefix = prefix[:0]
		clipped = false
		line++
	}
	if totalBytes == 0 {
		return renderReadPage(header, []string{"(empty file)"}, offset, 1, budget)
	}
	return renderReadPage(header, lines, offset, line, budget)
}

func renderReadPage(header string, lines []string, offset, total, budget int) (string, error) {
	if offset > total {
		return "", fmt.Errorf("offset %d is beyond end of file (%d lines total)", offset, total)
	}
	if len(header) < budget {
		cut := TruncateHead(lines, len(lines), budget-len(header))
		used := len(cut.Content) + len(header)
		for count := cut.ShownLines; count > 0; count-- {
			shownEnd := offset + count - 1
			notice := ""
			if shownEnd < total {
				notice = ReadTruncationNotice(offset, shownEnd, total, shownEnd+1)
			}
			if used+len(notice) <= budget {
				return header + strings.Join(lines[:count], "\n") + notice, nil
			}
			used -= len(lines[count-1]) + 1
		}
		// A long first line must not block access to all subsequent lines.
		if len(lines) > 0 {
			notice := ""
			if offset < total {
				notice = ReadTruncationNotice(offset, offset, total, offset+1)
			}
			marker := LineTruncatedMarker()
			if available := budget - len(header) - len(notice) - len(marker); available > 0 {
				prefix := UTF8SafePrefix(lines[0], available)
				if prefix != "" {
					return header + prefix + marker + notice, nil
				}
			}
		}
	}
	return "", fmt.Errorf("line %d and its continuation notice cannot fit the %d-byte output budget", offset, budget)
}
