package fs_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/commons-db-migrate-artifacts/fs"
)

func TestLocalFS_ImplementsFilesystemRW(t *testing.T) {
	tempDir := t.TempDir()
	localFS := fs.NewLocalFS(tempDir)
	defer localFS.Close()

	var _ fs.FilesystemRW = localFS
}

func TestLocalFS_WriteAndRead(t *testing.T) {
	tempDir := t.TempDir()
	localFS := fs.NewLocalFS(tempDir)
	defer localFS.Close()

	ctx := context.Background()
	testData := "hello world"
	testPath := "test/file.txt"

	// Write data
	info, err := localFS.Write(ctx, testPath, strings.NewReader(testData))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if info == nil {
		t.Fatal("Write returned nil FileInfo")
	}

	// Read data back
	reader, err := localFS.Read(ctx, testPath)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if string(data) != testData {
		t.Errorf("Read data mismatch: got %q, want %q", string(data), testData)
	}
}

func TestLocalFS_ReadDir(t *testing.T) {
	tempDir := t.TempDir()
	localFS := fs.NewLocalFS(tempDir)
	defer localFS.Close()

	ctx := context.Background()

	// Create test files
	files := []string{"file1.txt", "file2.txt", "dir/file3.txt"}
	for _, f := range files {
		_, err := localFS.Write(ctx, f, strings.NewReader("test content"))
		if err != nil {
			t.Fatalf("Failed to create test file %s: %v", f, err)
		}
	}

	// Read root directory
	entries, err := localFS.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}

	if len(entries) == 0 {
		t.Error("ReadDir returned no entries")
	}

	// Verify FullPath is set
	for _, entry := range entries {
		if entry.FullPath() == "" {
			t.Errorf("Entry %s has empty FullPath", entry.Name())
		}
	}
}

func TestLocalFS_Stat(t *testing.T) {
	tempDir := t.TempDir()
	localFS := fs.NewLocalFS(tempDir)
	defer localFS.Close()

	ctx := context.Background()
	testPath := "test.txt"
	testData := "test content"

	// Write file
	_, err := localFS.Write(ctx, testPath, strings.NewReader(testData))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Stat file
	info, err := localFS.Stat(testPath)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	if info.Name() != testPath {
		t.Errorf("Stat name mismatch: got %q, want %q", info.Name(), testPath)
	}

	if info.Size() != int64(len(testData)) {
		t.Errorf("Stat size mismatch: got %d, want %d", info.Size(), len(testData))
	}
}

func TestLocalFS_Read_NonExistentPath(t *testing.T) {
	tempDir := t.TempDir()
	localFS := fs.NewLocalFS(tempDir)
	defer localFS.Close()

	ctx := context.Background()

	_, err := localFS.Read(ctx, "nonexistent.txt")
	if err == nil {
		t.Error("Expected error for non-existent file, got nil")
	}
	if !os.IsNotExist(err) {
		t.Errorf("Expected os.ErrNotExist, got: %v", err)
	}
}

func TestLocalFS_ConcurrentWrites(t *testing.T) {
	tempDir := t.TempDir()
	localFS := fs.NewLocalFS(tempDir)
	defer localFS.Close()

	ctx := context.Background()
	numGoroutines := 10

	done := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			path := filepath.Join("concurrent", string(rune('a'+id))+".txt")
			data := strings.NewReader("concurrent write test")
			_, err := localFS.Write(ctx, path, data)
			done <- err
		}(i)
	}

	for i := 0; i < numGoroutines; i++ {
		if err := <-done; err != nil {
			t.Errorf("Concurrent write %d failed: %v", i, err)
		}
	}

	// Verify all files were created
	entries, err := localFS.ReadDir("concurrent")
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}

	if len(entries) != numGoroutines {
		t.Errorf("Expected %d files, got %d", numGoroutines, len(entries))
	}
}
