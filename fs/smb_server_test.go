// Tests the SMB filesystem against an SMB server named by COMMONS_DB_SMB_TEST_SERVER,
// covering writes, reads and file deletion.
package fs_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/commons-db/fs"
	"github.com/flanksource/commons-db/types"
)

// openSMBFS connects to the test share, or skips without a server. The share
// (COMMONS_DB_SMB_TEST_SHARE, default Data) must accept COMMONS_DB_SMB_TEST_USER and
// COMMONS_DB_SMB_TEST_PASSWORD (default samba/secret).
func openSMBFS(t *testing.T) deletableFS {
	t.Helper()
	server := os.Getenv("COMMONS_DB_SMB_TEST_SERVER")
	if server == "" {
		t.Skip("set COMMONS_DB_SMB_TEST_SERVER (host:port) to an SMB server to run SMB tests")
	}
	host, port, err := net.SplitHostPort(server)
	if err != nil {
		t.Fatalf("COMMONS_DB_SMB_TEST_SERVER %q is not host:port: %v", server, err)
	}
	env := func(name, fallback string) string {
		if value := os.Getenv(name); value != "" {
			return value
		}
		return fallback
	}
	auth := types.Authentication{
		Username: types.EnvVar{ValueStatic: env("COMMONS_DB_SMB_TEST_USER", "samba")},
		Password: types.EnvVar{ValueStatic: env("COMMONS_DB_SMB_TEST_PASSWORD", "secret")},
	}
	smbFS, err := fs.NewSMBFS(host, port, env("COMMONS_DB_SMB_TEST_SHARE", "Data"), auth)
	if err != nil {
		t.Fatalf("connect to the test SMB share: %v", err)
	}
	t.Cleanup(func() { _ = smbFS.Close() })
	return smbFS
}

func TestSMBFS_WriteAndRead(t *testing.T) {
	smbFS := openSMBFS(t)
	ctx := context.Background()
	name := fmt.Sprintf("commons-db-test-%d.txt", time.Now().UnixNano())
	if _, err := smbFS.Write(ctx, name, strings.NewReader("hello")); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	reader, err := smbFS.Read(ctx, name)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "hello" {
		t.Fatalf("Read: %q, %v", data, err)
	}
}

func TestSMBFS_Delete(t *testing.T) {
	smbFS := openSMBFS(t)
	ctx := context.Background()
	name := fmt.Sprintf("commons-db-test-%d.txt", time.Now().UnixNano())
	if _, err := smbFS.Write(ctx, name, strings.NewReader("data")); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if err := smbFS.Delete(ctx, name); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := smbFS.Stat(name); !os.IsNotExist(err) {
		t.Errorf("Stat after Delete: got %v, want a not-exist error", err)
	}
	if err := smbFS.Delete(ctx, name); err != nil {
		t.Errorf("deleting a missing file: got %v, want nil", err)
	}
}
