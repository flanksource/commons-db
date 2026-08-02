package shell

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloneFile pins the syscall wiring: on a reflink-capable filesystem
// (APFS, btrfs, XFS) cloneFile must succeed and produce an independent copy;
// anywhere else it must report errCloneUnsupported so copyPath falls back to a
// byte copy rather than propagating a hard failure.
func TestCloneFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	require.NoError(t, os.WriteFile(src, []byte("original\n"), 0644))

	err := cloneFile(src, dst)
	if errors.Is(err, errCloneUnsupported) {
		t.Skipf("filesystem backing %s has no copy-on-write clone", dir)
	}
	require.NoError(t, err)

	assert.Equal(t, "original\n", string(readFile(t, dst)))

	require.NoError(t, os.WriteFile(dst, []byte("rewritten\n"), 0644))
	assert.Equal(t, "original\n", string(readFile(t, src)), "clone shares storage with its source")
}

func TestCopyPathPrefersCloneAndReportsSize(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	require.NoError(t, os.WriteFile(src, []byte("hello world"), 0644))

	n, cloned, err := copyPath(src, filepath.Join(dir, "nested", "dst.txt"))
	require.NoError(t, err)
	assert.EqualValues(t, len("hello world"), n)
	assert.Equal(t, "hello world", string(readFile(t, filepath.Join(dir, "nested", "dst.txt"))))
	t.Logf("copy-on-write clone used: %v", cloned)
}
