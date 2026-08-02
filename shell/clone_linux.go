//go:build linux

package shell

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile uses the FICLONE ioctl, implemented as a copy-on-write clone by
// btrfs, XFS (reflink=1) and bcachefs.
func cloneFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()

	if err := unix.IoctlFileClone(int(out.Fd()), int(in.Fd())); err != nil {
		_ = os.Remove(dst)
		switch {
		case errors.Is(err, unix.EOPNOTSUPP), errors.Is(err, unix.EXDEV),
			errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOTTY):
			return errCloneUnsupported
		default:
			return err
		}
	}
	return nil
}
