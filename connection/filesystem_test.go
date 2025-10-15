package connection_test

import (
	"testing"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/fs"
	dbctx "github.com/flanksource/commons-db/context"
)

func TestGetFilesystemForConnection_String(t *testing.T) {
	ctx := dbctx.NewContext(nil)

	// Test with string path (local filesystem)
	filesystem, err := connection.GetFilesystemForConnection(ctx, "/tmp/test")
	if err != nil {
		t.Fatalf("GetFilesystemForConnection failed: %v", err)
	}
	defer filesystem.Close()

	if filesystem == nil {
		t.Fatal("Expected non-nil filesystem")
	}

	var _ fs.FilesystemRW = filesystem
}

func TestGetFilesystemForConnection_Nil(t *testing.T) {
	ctx := dbctx.NewContext(nil)

	_, err := connection.GetFilesystemForConnection(ctx, nil)
	if err == nil {
		t.Error("Expected error for nil connection, got nil")
	}
}

func TestGetFilesystemForConnection_UnsupportedType(t *testing.T) {
	ctx := dbctx.NewContext(nil)

	_, err := connection.GetFilesystemForConnection(ctx, 123)
	if err == nil {
		t.Error("Expected error for unsupported type, got nil")
	}
}

func TestGetFilesystem_FilesystemProvider(t *testing.T) {
	ctx := dbctx.NewContext(nil)

	// Create a local path connection
	localConn := "/tmp/test"

	filesystem, err := connection.GetFilesystemForConnection(ctx, localConn)
	if err != nil {
		t.Fatalf("GetFilesystemForConnection failed: %v", err)
	}
	defer filesystem.Close()

	// Verify it implements FilesystemRW
	var _ fs.FilesystemRW = filesystem
}
