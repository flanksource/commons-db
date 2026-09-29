// The filesystem type of a directory on macOS, as statfs names it.
package owner

import "golang.org/x/sys/unix"

func filesystemName(dir string) (string, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		return "", err
	}
	return unix.ByteSliceToString(stat.Fstypename[:]), nil
}
