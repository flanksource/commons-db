// On platforms without a filesystem name to check, every directory is
// accepted.

//go:build !linux && !darwin

package owner

func filesystemName(string) (string, error) { return "", nil }
