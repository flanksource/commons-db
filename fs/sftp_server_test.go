// Tests the SFTP filesystem against an in-process SSH server that serves a temp directory,
// so writes, deletes and leaked file handles are checked without an external SFTP host.
package fs_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/flanksource/commons-db/fs"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	sftpTestUser     = "tester"
	sftpTestPassword = "secret"
)

// startSFTPServer serves root over SFTP on a loopback port and trusts its host key through
// SSH_KNOWN_HOSTS. It returns the server's address.
func startSFTPServer(t *testing.T, root string) string {
	t.Helper()
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatalf("host key signer: %v", err)
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if meta.User() == sftpTestUser && string(password) == sftpTestPassword {
				return nil, nil
			}
			return nil, fmt.Errorf("wrong password for %s", meta.User())
		},
	}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveSFTP(conn, config, root)
		}
	}()

	addr := listener.Addr().(*net.TCPAddr)
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	line := fmt.Sprintf("[127.0.0.1]:%d %s", addr.Port, ssh.MarshalAuthorizedKey(signer.PublicKey()))
	if err := os.WriteFile(knownHosts, []byte(line), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	t.Setenv("SSH_KNOWN_HOSTS", knownHosts)
	return addr.String()
}

func serveSFTP(conn net.Conn, config *ssh.ServerConfig, root string) {
	defer conn.Close()
	_, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only sessions are served")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			return
		}
		go func() {
			for req := range channelRequests {
				_ = req.Reply(req.Type == "subsystem" && string(req.Payload[4:]) == "sftp", nil)
			}
		}()
		server, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(root))
		if err != nil {
			_ = channel.Close()
			return
		}
		_ = server.Serve()
		_ = server.Close()
	}
}

func openSFTPFS(t *testing.T) (fs.FilesystemRW, string) {
	t.Helper()
	root := t.TempDir()
	sftpFS, err := fs.NewSFTPFS(startSFTPServer(t, root), sftpTestUser, sftpTestPassword)
	if err != nil {
		t.Fatalf("connect to the test SFTP server: %v", err)
	}
	t.Cleanup(func() { _ = sftpFS.Close() })
	return sftpFS, root
}

func TestSFTPFS_WriteAndRead(t *testing.T) {
	sftpFS, root := openSFTPFS(t)
	ctx := context.Background()
	if _, err := sftpFS.Write(ctx, "dir/file.txt", strings.NewReader("hello")); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	onDisk, err := os.ReadFile(filepath.Join(root, "dir", "file.txt"))
	if err != nil || string(onDisk) != "hello" {
		t.Fatalf("file on the server: %q, %v", onDisk, err)
	}
	reader, err := sftpFS.Read(ctx, "dir/file.txt")
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "hello" {
		t.Fatalf("Read: %q, %v", data, err)
	}
}

func TestSFTPFS_WriteClosesTheFile(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("counting open files needs /proc/self/fd")
	}
	// The server runs in this process, so a remote handle left open is a file open here.
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	sftpFS, _ := openSFTPFS(t)
	openFiles := func() int {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatalf("list open files: %v", err)
		}
		return len(entries)
	}
	before := openFiles()
	for i := range 10 {
		if _, err := sftpFS.Write(context.Background(), fmt.Sprintf("file-%d.txt", i), strings.NewReader("data")); err != nil {
			t.Fatalf("Write failed: %v", err)
		}
	}
	if leaked := openFiles() - before; leaked > 0 {
		t.Errorf("10 writes left %d files open on the server", leaked)
	}
}

func TestSFTPFS_Delete(t *testing.T) {
	sftpFS, root := openSFTPFS(t)
	ctx := context.Background()
	if _, err := sftpFS.Write(ctx, "dir/file.txt", strings.NewReader("data")); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if err := sftpFS.Delete(ctx, "dir/file.txt"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "dir", "file.txt")); !os.IsNotExist(err) {
		t.Errorf("file on the server after Delete: got %v, want a not-exist error", err)
	}
	if err := sftpFS.Delete(ctx, "dir/file.txt"); err != nil {
		t.Errorf("deleting a missing file: got %v, want nil", err)
	}
}

func TestSFTPFS_ImplementsFilesystemRW(t *testing.T) {
	// Note: Requires actual SFTP server for initialization
	// This test verifies the interface compliance only
	t.Skip("Requires SFTP server - skipping interface test")

	sftpFS, err := fs.NewSFTPFS("localhost:22", "user", "pass")
	if err != nil {
		t.Skipf("Failed to create SFTP FS: %v", err)
	}
	defer sftpFS.Close()

	var _ fs.FilesystemRW = sftpFS
}
