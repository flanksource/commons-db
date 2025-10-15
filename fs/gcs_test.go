package fs_test

import (
	"testing"

	"github.com/flanksource/commons-db/fs"
	dbctx "github.com/flanksource/commons-db/context"
)

func TestGCSFS_ImplementsFilesystemRW(t *testing.T) {
	ctx := dbctx.NewContext(nil)
	// Note: Requires actual GCS client for full initialization
	// This test verifies the interface compliance only
	t.Skip("Requires GCS client - skipping interface test")

	gcsFS := fs.NewGCSFS(ctx, "test-bucket", nil)
	defer gcsFS.Close()

	var _ fs.FilesystemRW = gcsFS
}
