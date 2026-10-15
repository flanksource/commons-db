// Tests the GCS filesystem against a GCS-compatible server named by COMMONS_DB_GCS_TEST_ENDPOINT,
// such as fake-gcs-server, covering writes, reads and object deletion.
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
)

// gcsTestFilesystem opens the GCS filesystem for the test server, or skips without one. The
// bucket (COMMONS_DB_GCS_TEST_BUCKET, default commons-db-test) must already exist.
func gcsTestFilesystem(t *testing.T) fs.FilesystemRW {
	t.Helper()
	endpoint := os.Getenv("COMMONS_DB_GCS_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("set COMMONS_DB_GCS_TEST_ENDPOINT to a GCS-compatible server (e.g. http://127.0.0.1:4443/storage/v1/) to run GCS tests")
	}
	bucket := os.Getenv("COMMONS_DB_GCS_TEST_BUCKET")
	if bucket == "" {
		bucket = "commons-db-test"
	}
	conn := connection.GCSConnection{GCPConnection: connection.GCPConnection{Endpoint: endpoint}, Bucket: bucket}
	filesystem, err := conn.Filesystem(dbctx.NewContext(context.Background()))
	if err != nil {
		t.Fatalf("open the GCS filesystem: %v", err)
	}
	t.Cleanup(func() { _ = filesystem.Close() })
	return filesystem
}

func TestGCSFilesystem_WriteAndRead(t *testing.T) {
	filesystem := gcsTestFilesystem(t)
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

func TestGCSFilesystem_Delete(t *testing.T) {
	filesystem := gcsTestFilesystem(t)
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
