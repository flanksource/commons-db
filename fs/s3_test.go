//go:build !fast

package fs_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	dbctx "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/fs"
)

func TestS3FS_ImplementsFilesystemRW(t *testing.T) {
	ctx := dbctx.NewContext(context.Background())
	cfg := aws.Config{}
	s3FS := fs.NewS3FS(ctx, "test-bucket", cfg)
	defer s3FS.Close()

	var _ fs.FilesystemRW = s3FS
}
