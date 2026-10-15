package fs

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// localFS implements FilesystemRW for local filesystem
type localFS struct {
	base string
}

type localFileInfo struct {
	os.FileInfo
	fullpath string
}

func (t localFileInfo) FullPath() string {
	return t.fullpath
}

func NewLocalFS(base string) *localFS {
	return &localFS{base: base}
}

func (t *localFS) Close() error {
	return nil
}

func (t *localFS) ReadDir(name string) ([]FileInfo, error) {
	if strings.Contains(name, "*") {
		return t.ReadDirGlob(name)
	}

	path := filepath.Join(t.base, name)
	files, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	output := make([]FileInfo, 0, len(files))
	for _, match := range files {
		fullPath := filepath.Join(path, match.Name())
		info, err := os.Stat(fullPath)
		if err != nil {
			return nil, err
		}

		output = append(output, localFileInfo{FileInfo: info, fullpath: fullPath})
	}

	return output, nil
}

func (t *localFS) ReadDirGlob(name string) ([]FileInfo, error) {
	base, pattern := doublestar.SplitPattern(filepath.Join(t.base, name))
	matches, err := doublestar.Glob(os.DirFS(base), pattern)
	if err != nil {
		return nil, err
	}

	output := make([]FileInfo, 0, len(matches))
	for _, match := range matches {
		fullPath := filepath.Join(base, match)
		info, err := os.Stat(fullPath)
		if err != nil {
			return nil, err
		}

		output = append(output, localFileInfo{FileInfo: info, fullpath: fullPath})
	}

	return output, nil
}

func (t *localFS) Stat(name string) (os.FileInfo, error) {
	return os.Stat(filepath.Join(t.base, name))
}

func (t *localFS) Read(ctx context.Context, path string) (io.ReadCloser, error) {
	return os.Open(filepath.Join(t.base, path))
}

func (t *localFS) Write(ctx context.Context, path string, data io.Reader) (os.FileInfo, error) {
	fullpath := filepath.Join(t.base, path)

	// Ensure the directory exists
	err := os.MkdirAll(filepath.Dir(fullpath), os.ModePerm)
	if err != nil {
		return nil, fmt.Errorf("error creating base directory: %w", err)
	}

	f, err := os.Create(fullpath)
	if err != nil {
		return nil, err
	}

	if _, err := io.Copy(f, data); err != nil {
		_ = f.Close()
		return nil, err
	}

	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("error closing %s: %w", fullpath, err)
	}

	return t.Stat(path)
}

// Delete removes the file at path. A missing file is not an error.
func (t *localFS) Delete(ctx context.Context, path string) error {
	if err := os.Remove(filepath.Join(t.base, path)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
