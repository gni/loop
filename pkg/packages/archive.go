package packages

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"loop/pkg/storage"
)

const maxArchiveBytes = 64 << 20
const maxArchiveEntries = 16384

type archiveInput struct {
	ctx    context.Context
	reader io.Reader
}

func (input archiveInput) Read(data []byte) (int, error) {
	if err := input.ctx.Err(); err != nil {
		return 0, err
	}
	return input.reader.Read(data)
}

func unpack(ctx context.Context, archivePath, destination string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := storage.OpenRegularFile(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	// Leave room for compression overhead while bounding gzip header parsing.
	if info.Size() > maxArchiveBytes+(1<<20) {
		return fmt.Errorf("compressed package archive exceeds 65 MiB")
	}
	compressed, err := gzip.NewReader(archiveInput{ctx, file})
	if err != nil {
		return err
	}
	defer compressed.Close()
	// One extra byte distinguishes the limit from the real end of the stream.
	decoded := &io.LimitedReader{R: archiveInput{ctx, compressed}, N: maxArchiveBytes + 1}
	reader := tar.NewReader(decoded)
	total := int64(0)
	entries := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if decoded.N == 0 {
			return fmt.Errorf("package archive exceeds 64 MiB")
		}
		if err == io.EOF {
			// tar ends before gzip's trailer. Finish reading to verify its checksum
			// and count padding or additional gzip members against the same limit.
			_, err := io.Copy(io.Discard, decoded)
			if decoded.N == 0 {
				return fmt.Errorf("package archive exceeds 64 MiB")
			}
			return err
		}
		if err != nil {
			return err
		}
		entries++
		if entries > maxArchiveEntries {
			return fmt.Errorf("package archive exceeds %d entries", maxArchiveEntries)
		}
		name := strings.TrimPrefix(header.Name, "package/")
		if header.Name == "package" {
			name = ""
		}
		// TAR paths use forward slashes. Reject drive names, alternate streams
		// and backslashes consistently on every supported operating system.
		if strings.ContainsAny(name, "\\:") {
			return fmt.Errorf("invalid package archive path %q", header.Name)
		}
		clean := path.Clean(name)
		localPath := filepath.FromSlash(clean)
		if !filepath.IsLocal(localPath) {
			return fmt.Errorf("archive path escapes package directory")
		}
		if clean == "." {
			if header.Typeflag != tar.TypeDir {
				return fmt.Errorf("package archive root must be a directory")
			}
			continue
		}
		target := filepath.Join(destination, localPath)
		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("package archive contains an unsupported link or special file")
		}
		if header.Size < 0 || header.Size > maxArchiveBytes-total {
			return fmt.Errorf("package archive exceeds 64 MiB")
		}
		total += header.Size
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, os.FileMode(header.Mode)&0700)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(output, reader, header.Size)
		closeErr := output.Close()
		if decoded.N == 0 {
			return fmt.Errorf("package archive exceeds 64 MiB")
		}
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
}
