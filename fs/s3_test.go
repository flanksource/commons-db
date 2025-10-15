//go:build !fast

package fs_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/flanksource/commons-db/fs"
	dbctx "github.com/flanksource/commons-db/context"
)

func TestS3FS_ImplementsFilesystemRW(t *testing.T) {
	ctx := dbctx.NewContext(nil)
	cfg := aws.Config{}
	s3FS := fs.NewS3FS(ctx, "test-bucket", cfg)
	defer s3FS.Close()

	var _ fs.FilesystemRW = s3FS
}
