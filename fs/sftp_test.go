package fs_test

import (
	"testing"

	"github.com/flanksource/commons-db/fs"
)

func TestSFTPFS_ImplementsFilesystemRW(t *testing.T) {
	// Note: Requires actual SFTP server for initialization
	// This test verifies the interface compliance only
	t.Skip("Requires SFTP server - skipping interface test")

	sftpFS, err := fs.NewSFTPFS("localhost:22", "user", "pass")
	if err != nil {
		t.Skipf("Failed to create SFTP FS: %v", err)
	}
	defer sftpFS.Close()

	var _ fs.FilesystemRW = sftpFS
}
