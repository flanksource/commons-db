package connection

import (
	"fmt"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/fs"
)

// FilesystemProvider is implemented by connections that can provide a filesystem interface
type FilesystemProvider interface {
	Filesystem(ctx context.Context) (fs.FilesystemRW, error)
}

// GetFilesystem returns a filesystem interface from any connection that implements FilesystemProvider
func GetFilesystem(ctx context.Context, conn FilesystemProvider) (fs.FilesystemRW, error) {
	if conn == nil {
		return nil, fmt.Errorf("connection is nil")
	}
	return conn.Filesystem(ctx)
}

// GetFilesystemForConnection is a helper that attempts to get a filesystem from various connection types
func GetFilesystemForConnection(ctx context.Context, conn interface{}) (fs.FilesystemRW, error) {
	switch c := conn.(type) {
	case FilesystemProvider:
		return c.Filesystem(ctx)
	case *S3Connection:
		return c.Filesystem(ctx)
	case *GCSConnection:
		return c.Filesystem(ctx)
	case *SFTPConnection:
		return c.Filesystem(ctx)
	case *SMBConnection:
		return c.Filesystem(ctx)
	case string:
		// Treat string as local filesystem path
		return fs.NewLocalFS(c), nil
	default:
		return nil, fmt.Errorf("unsupported connection type: %T", conn)
	}
}
