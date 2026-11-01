//go:build !darwin && !linux

package workspace

// cloneFile has no copy-on-write primitive to reach for on this platform.
func cloneFile(_, _ string) error {
	return errCloneUnsupported
}
