package report

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// archiveSkip are directories never worth sending: build output and caches that
// the render server rebuilds anyway, and which dwarf the template itself.
var archiveSkip = map[string]bool{
	"node_modules": true,
	".facet":       true,
	".git":         true,
	"dist":         true,
}

// BuildArchive tars and gzips a template directory for the facet server.
func BuildArchive(srcDir string) ([]byte, error) {
	if strings.TrimSpace(srcDir) == "" {
		return nil, fmt.Errorf("template directory is required")
	}
	info, err := os.Stat(srcDir)
	if err != nil {
		return nil, fmt.Errorf("template directory %q: %w", srcDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("template path %q is not a directory", srcDir)
	}

	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)

	walkErr := filepath.WalkDir(srcDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.IsDir() {
			if archiveSkip[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		return writeArchiveFile(tarWriter, path, relative, entry)
	})
	if walkErr != nil {
		return nil, fmt.Errorf("archive template %q: %w", srcDir, walkErr)
	}

	if err := tarWriter.Close(); err != nil {
		return nil, fmt.Errorf("close tar writer: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, fmt.Errorf("close gzip writer: %w", err)
	}
	return buffer.Bytes(), nil
}

func writeArchiveFile(writer *tar.Writer, path, relative string, entry fs.DirEntry) error {
	info, err := entry.Info()
	if err != nil {
		return err
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(relative)
	if err := writer.WriteHeader(header); err != nil {
		return err
	}

	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.Copy(writer, file)
	return err
}
