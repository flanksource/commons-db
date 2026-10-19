package dbtest

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const linuxSharedMemory = "/dev/shm"

// ramBackedDataDir returns a directory on /dev/shm after checking that it is a
// tmpfs at least size bytes large. /dev/shm always exists, so no lock is needed.
func ramBackedDataDir(name string, size uint64, _ string) (string, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(linuxSharedMemory, &stat); err != nil {
		return "", fmt.Errorf("statfs %s: %w", linuxSharedMemory, err)
	}
	if err := validateTmpfs(linuxSharedMemory, filesystemStat{
		Tmpfs:         stat.Type == unix.TMPFS_MAGIC,
		CapacityBytes: stat.Blocks * uint64(stat.Bsize), //nolint:gosec // block size is positive
	}, size); err != nil {
		return "", err
	}
	return filepath.Join(linuxSharedMemory, name), nil
}
