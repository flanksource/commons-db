// Tests the S3 filesystem against an S3-compatible server named by COMMONS_DB_S3_TEST_ENDPOINT,
// covering the connection's endpoint and path-style options and object deletion.
package connection_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/commons-db/connection"
	dbctx "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/fs"
	"github.com/flanksource/commons-db/types"
)

// s3TestFilesystem opens the S3 filesystem for the test server, or skips without one.
func s3TestFilesystem(t *testing.T) fs.FilesystemRW {
	t.Helper()
	endpoint := os.Getenv("COMMONS_DB_S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("set COMMONS_DB_S3_TEST_ENDPOINT to an S3-compatible server to run S3 tests")
	}
	bucket := os.Getenv("COMMONS_DB_S3_TEST_BUCKET")
	if bucket == "" {
		bucket = "commons-db-test"
	}
	conn := connection.S3Connection{
		AWSConnection: connection.AWSConnection{
			Endpoint:  endpoint,
			Region:    "us-east-1",
			AccessKey: types.EnvVar{ValueStatic: "test"},
			SecretKey: types.EnvVar{ValueStatic: "test"},
		},
		Bucket:       bucket,
		UsePathStyle: true,
	}
	filesystem, err := conn.Filesystem(dbctx.NewContext(context.Background()))
	if err != nil {
		t.Fatalf("open the S3 filesystem: %v", err)
	}
	t.Cleanup(func() { _ = filesystem.Close() })
	return filesystem
}

func TestS3Filesystem_UsesTheConnectionEndpoint(t *testing.T) {
	filesystem := s3TestFilesystem(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	key := fmt.Sprintf("commons-db-test/%d/file.txt", time.Now().UnixNano())

	if _, err := filesystem.Write(ctx, key, strings.NewReader("hello")); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	reader, err := filesystem.Read(ctx, key)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "hello" {
		t.Fatalf("Read: %q, %v", data, err)
	}
}

func TestS3Filesystem_Delete(t *testing.T) {
	filesystem := s3TestFilesystem(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	key := fmt.Sprintf("commons-db-test/%d/delete.txt", time.Now().UnixNano())
	if _, err := filesystem.Write(ctx, key, strings.NewReader("data")); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if err := filesystem.Delete(ctx, key); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := filesystem.Stat(key); err == nil {
		t.Errorf("Stat after Delete found %s", key)
	}
	if err := filesystem.Delete(ctx, key); err != nil {
		t.Errorf("deleting a missing object: got %v, want nil", err)
	}
}
