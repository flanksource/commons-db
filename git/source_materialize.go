package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type sourceEntry struct{ path, mode, object string }

func (s *PreparedSource) entries(ctx context.Context) ([]sourceEntry, error) {
	raw, err := s.git(ctx, "ls-tree", "-r", "-z", "--full-tree", s.info.TreeID)
	if err != nil {
		return nil, err
	}
	entries := []sourceEntry{}
	for _, record := range strings.Split(raw, "\x00") {
		if record == "" {
			continue
		}
		header, path, ok := strings.Cut(record, "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("invalid Git tree entry %q", record)
		}
		if _, err := safeJoin(s.info.Dir, path); err != nil {
			return nil, err
		}
		if fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755" && fields[0] != "120000") {
			return nil, fmt.Errorf("%w: mode %s at %q", ErrUnsupportedSource, fields[0], path)
		}
		entries = append(entries, sourceEntry{path: path, mode: fields[0], object: fields[2]})
	}
	return entries, nil
}

func (s *PreparedSource) materialize(ctx context.Context) (err error) {
	entries, err := s.entries(ctx)
	if err != nil {
		return err
	}
	if err := os.Mkdir(s.info.Dir, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(s.info.Dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if err := s.streamBlobs(ctx, root, entries); err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.mode != "120000" {
			continue
		}
		_, err := root.Stat(entry.path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: symlink %q: %w", ErrUnsupportedSource, entry.path, err)
		}
	}
	return nil
}

func (s *PreparedSource) streamBlobs(ctx context.Context, root *os.Root, entries []sourceEntry) (err error) {
	objects := make([]string, len(entries))
	for index, entry := range entries {
		objects[index] = entry.object
	}
	return readObjectBatch(ctx, objectBatchOptions{Dir: s.gitDir, Env: s.env, Objects: objects, Read: func(index int, blob batchBlob, content io.Reader) error {
		if blob.Missing {
			return fmt.Errorf("git source blob %q is missing", entries[index].path)
		}
		return writeSourceEntry(root, content, entries[index], blob.Size)
	}})
}

func writeSourceEntry(root *os.Root, reader io.Reader, entry sourceEntry, size int64) error {
	if err := root.MkdirAll(filepath.Dir(entry.path), 0o700); err != nil {
		return err
	}
	if entry.mode == "120000" {
		target, err := io.ReadAll(io.LimitReader(reader, size))
		if err != nil {
			return err
		}
		if filepath.IsAbs(string(target)) || !filepath.IsLocal(filepath.Join(filepath.Dir(entry.path), string(target))) {
			return fmt.Errorf("%w: symlink %q escapes the source", ErrUnsupportedSource, entry.path)
		}
		return root.Symlink(string(target), entry.path)
	}
	mode := os.FileMode(0o644)
	if entry.mode == "100755" {
		mode = 0o755
	}
	file, err := root.OpenFile(entry.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.CopyN(file, reader, size)
	return errors.Join(copyErr, file.Close())
}
