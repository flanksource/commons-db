//go:build !linux && !darwin

package db

import (
	"fmt"
	"runtime"
)

func detectMemoryBytes() (uint64, error) {
	return 0, fmt.Errorf("detect host memory: unsupported on %s", runtime.GOOS)
}
