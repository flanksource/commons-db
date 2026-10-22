package db

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func detectMemoryBytes() (uint64, error) {
	return sysctlMemoryBytes("hw.memsize")
}

func sysctlMemoryBytes(name string) (uint64, error) {
	memory, err := unix.SysctlUint64(name)
	if err != nil {
		return 0, fmt.Errorf("read sysctl %s: %w", name, err)
	}
	return memory, nil
}
