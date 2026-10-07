package connection_test

import (
	"context"
	"testing"

	"github.com/flanksource/commons-db/connection"
	dbctx "github.com/flanksource/commons-db/context"
)

func TestGetFilesystemForConnection_String(t *testing.T) {
	ctx := dbctx.NewContext(context.Background())

	// Test with string path (local filesystem)
	filesystem, err := connection.GetFilesystemForConnection(ctx, "/tmp/test")
	if err != nil {
		t.Fatalf("GetFilesystemForConnection failed: %v", err)
	}
	defer filesystem.Close()

	if filesystem == nil {
		t.Fatal("Expected non-nil filesystem")
	}
}

func TestGetFilesystemForConnection_Nil(t *testing.T) {
	ctx := dbctx.NewContext(context.Background())

	_, err := connection.GetFilesystemForConnection(ctx, nil)
	if err == nil {
		t.Error("Expected error for nil connection, got nil")
	}
}

func TestGetFilesystemForConnection_UnsupportedType(t *testing.T) {
	ctx := dbctx.NewContext(context.Background())

	_, err := connection.GetFilesystemForConnection(ctx, 123)
	if err == nil {
		t.Error("Expected error for unsupported type, got nil")
	}
}

func TestGetFilesystem_FilesystemProvider(t *testing.T) {
	ctx := dbctx.NewContext(context.Background())

	// Create a local path connection
	localConn := "/tmp/test"

	filesystem, err := connection.GetFilesystemForConnection(ctx, localConn)
	if err != nil {
		t.Fatalf("GetFilesystemForConnection failed: %v", err)
	}
	defer filesystem.Close()
}
