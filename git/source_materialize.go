package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	finish := startGitTiming(ctx, "commands")
	defer func() { finish(err) }()
	var input strings.Builder
	for _, entry := range entries {
		input.WriteString(entry.object + "\n")
	}
	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch")
	cmd.Dir, cmd.Env, cmd.Stdin = s.gitDir, s.env, strings.NewReader(input.String())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	reader := bufio.NewReader(pipe)
	for _, entry := range entries {
		if err := writeSourceBlob(root, reader, entry); err != nil {
			killErr := cmd.Process.Kill()
			return errors.Join(err, killErr, cmd.Wait())
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git cat-file: %w: %s", err, Redact(stderr.String()))
	}
	return nil
}

func writeSourceBlob(root *os.Root, reader *bufio.Reader, entry sourceEntry) error {
	header, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read blob for %q: %w", entry.path, err)
	}
	fields := strings.Fields(header)
	if len(fields) != 3 || fields[0] != entry.object || fields[1] != "blob" {
		return fmt.Errorf("invalid Git blob for %q: %q", entry.path, header)
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return fmt.Errorf("invalid Git blob size for %q: %q", entry.path, header)
	}
	if err := writeSourceEntry(root, reader, entry, size); err != nil {
		return err
	}
	if ending, err := reader.ReadByte(); err != nil || ending != '\n' {
		return fmt.Errorf("invalid Git blob terminator for %q", entry.path)
	}
	return nil
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
