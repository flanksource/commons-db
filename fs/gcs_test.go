package fs_test

import (
	"context"
	"testing"

	dbctx "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/fs"
)

func TestGCSFS_ImplementsFilesystemRW(t *testing.T) {
	// Note: Requires actual GCS client for full initialization
	// This test verifies the interface compliance only
	t.Skip("Requires GCS client - skipping interface test")

	ctx := dbctx.NewContext(context.Background())
	gcsFS := fs.NewGCSFS(ctx, "test-bucket", nil)
	defer gcsFS.Close()

	var _ fs.FilesystemRW = gcsFS
}
