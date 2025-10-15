package fs

import (
	"context"
	"io"
	"os"
)

// FileInfo is a wrapper for os.FileInfo that also returns the full path of the file.
//
// FullPath is required to support globs with ReadDir()
type FileInfo interface {
	os.FileInfo
	FullPath() string
}

type ListItemLimiter interface {
	SetMaxListItems(maxList int)
}

type Filesystem interface {
	Close() error
	ReadDir(name string) ([]FileInfo, error)
	Stat(name string) (os.FileInfo, error)
}

type FilesystemRW interface {
	Filesystem
	Read(ctx context.Context, path string) (io.ReadCloser, error)
	Write(ctx context.Context, path string, data io.Reader) (os.FileInfo, error)
}
