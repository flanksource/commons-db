// Sealed streams kept gzip-compressed: a sealed stream's file is rewritten as
// <file>.gz, read linearly, and written plain again when the stream reopens.
package ndjson

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// gzipSuffix marks a data file kept compressed.
const gzipSuffix = ".gz"

func compressed(state sidecar) bool { return strings.HasSuffix(state.File, gzipSuffix) }

// linesFrom opens state's data file positioned at the line of seq first, and
// returns a reader of its committed lines from there, and how many committed
// bytes of the uncompressed lines come before it. A plain file is bisected to
// the line; a gzip one is read through the lines before it, which start at the
// stream's low seq, one line per seq.
func (b *Backend) linesFrom(state sidecar, first int64) (*bufio.Reader, int64, io.Closer, error) {
	path := b.dataPath(state)
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("stream %q: open %s: %w", state.Stream, path, err)
	}
	if !compressed(state) {
		offset, err := lineAfter(file, state.Bytes, first-1)
		if err != nil {
			return nil, 0, nil, errors.Join(fmt.Errorf("stream %q: find seq %d in %s: %w", state.Stream, first, path, err), file.Close())
		}
		return bufio.NewReader(io.NewSectionReader(file, offset, state.Bytes-offset)), offset, file, nil
	}
	decompressed, err := gzip.NewReader(file)
	if err != nil {
		return nil, 0, nil, errors.Join(fmt.Errorf("stream %q: read %s: %w", state.Stream, path, err), file.Close())
	}
	reader := bufio.NewReader(decompressed)
	var skipped int64
	for seq := state.LowSeq; seq < first; seq++ {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return nil, 0, nil, errors.Join(fmt.Errorf("stream %q: %s: seq %d: %w", state.Stream, path, seq, err), file.Close())
		}
		skipped += int64(len(line))
	}
	return reader, skipped, file, nil
}

// compressFile rewrites state's data file compressed, commits the sidecar
// naming it, and removes the plain file, returning the new sidecar. The
// compressed file is written under a temporary name and renamed into place,
// so a crash leaves the plain stream and at most an orphan file.
func (b *Backend) compressFile(state sidecar) (sidecar, error) {
	source := b.dataPath(state)
	packed := state
	packed.File = state.File + gzipSuffix
	if err := b.rewrite(state, packed, func(w io.Writer) (io.WriteCloser, error) { return gzip.NewWriter(w), nil }); err != nil {
		return sidecar{}, err
	}
	return packed, removeFile(source)
}

// decompressFile rewrites a compressed data file plain again, for a stream
// being appended to once more.
func (b *Backend) decompressFile(state sidecar) (sidecar, error) {
	source := b.dataPath(state)
	unpacked := state
	unpacked.File = strings.TrimSuffix(state.File, gzipSuffix)
	if err := b.rewrite(state, unpacked, func(w io.Writer) (io.WriteCloser, error) { return nopWriteCloser{w}, nil }); err != nil {
		return sidecar{}, err
	}
	return unpacked, removeFile(source)
}

// rewrite writes state's committed lines through wrap into target's data
// file and commits target's sidecar.
func (b *Backend) rewrite(state, target sidecar, wrap func(io.Writer) (io.WriteCloser, error)) error {
	lines, _, source, err := b.linesFrom(state, state.LowSeq)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	path := b.dataPath(target)
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("stream %q: create %s: %w", state.Stream, path, err)
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	writer, err := wrap(temporary)
	if err != nil {
		return errors.Join(err, temporary.Close())
	}
	_, copyErr := io.Copy(writer, lines)
	if err := errors.Join(copyErr, writer.Close(), temporary.Sync(), temporary.Close()); err != nil {
		return fmt.Errorf("stream %q: write %s: %w", state.Stream, path, err)
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return fmt.Errorf("stream %q: place %s: %w", state.Stream, path, err)
	}
	return b.writeSidecar(target)
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
