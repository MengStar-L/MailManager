//go:build !unix

package updater

import (
	"fmt"
	"io"
	"os"
)

func readRegularFileNoFollow(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("file must be a regular file no larger than %d bytes", limit)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return content, nil
}

func readRootOwnedRegularFileNoFollow(path string, limit int64) ([]byte, error) {
	return readRegularFileNoFollow(path, limit)
}
