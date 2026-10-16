//go:build !linux && !darwin

package dbtest

import (
	"fmt"
	"runtime"
)

func ramBackedDataDir(string, uint64, string) (string, error) {
	return "", fmt.Errorf("%s=tmpfs is unsupported on %s; set it to a path", EnvDataDir, runtime.GOOS)
}
