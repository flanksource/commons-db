//go:build darwin

package shell

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile uses clonefile(2), which APFS implements as a copy-on-write clone.
func cloneFile(src, dst string) error {
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}

	return cloneFileError(unix.Clonefile(src, dst, 0))
}

func cloneFileError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.ENOTSUP), errors.Is(err, unix.EXDEV), errors.Is(err, unix.EINVAL):
		return errCloneUnsupported
	default:
		return err
	}
}
