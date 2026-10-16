package db

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func detectMemoryBytes() (uint64, error) {
	memory, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, fmt.Errorf("read sysctl hw.memsize: %w", err)
	}
	return memory, nil
}
