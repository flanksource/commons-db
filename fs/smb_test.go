package fs_test

import (
	"testing"

	"github.com/flanksource/commons-db-migrate-artifacts/fs"
	"github.com/flanksource/commons-db/types"
)

func TestSMBFS_ImplementsFilesystemRW(t *testing.T) {
	// Note: Requires actual SMB server for initialization
	// This test verifies the interface compliance only
	t.Skip("Requires SMB server - skipping interface test")

	auth := types.Authentication{}
	smbFS, err := fs.NewSMBFS("localhost", "445", "share", auth)
	if err != nil {
		t.Skipf("Failed to create SMB FS: %v", err)
	}
	defer smbFS.Close()

	var _ fs.FilesystemRW = smbFS
}
