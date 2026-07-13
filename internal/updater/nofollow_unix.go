//go:build unix

package updater

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func readRegularFileNoFollow(path string, limit int64) ([]byte, error) {
	return readRegularFileNoFollowOwner(path, limit, false)
}

func readRootOwnedRegularFileNoFollow(path string, limit int64) ([]byte, error) {
	return readRegularFileNoFollowOwner(path, limit, true)
}

func readRegularFileNoFollowOwner(path string, limit int64, requireRoot bool) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open update request")
	}
	defer file.Close()
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		return nil, err
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Size > limit {
		return nil, fmt.Errorf("file must be a regular file no larger than %d bytes", limit)
	}
	if requireRoot && info.Uid != 0 {
		return nil, errors.New("file must be owned by root")
	}
	if requireRoot && info.Mode&0o022 != 0 {
		return nil, errors.New("root-owned file must not be writable by group or others")
	}
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return content, nil
}
