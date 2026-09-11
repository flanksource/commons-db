package ndjson

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// linePrefix opens every line the backend writes: encodeLines marshals a line
// struct whose first field is the seq.
var linePrefix = []byte(`{"seq":`)

// seekChunk is how much of the file one probe reads while looking for the end
// of the line it landed in.
const seekChunk = 4096

// lineAfter is the offset of the first line whose seq is past afterSeq, or size
// when there is none. It bisects the file by byte offset — the seq of the first
// line starting at or after an offset only grows with the offset — so an
// incremental scan resumes in O(log size) reads instead of parsing every line
// before it. The file is its own index: there is no offset to record, and so no
// second copy of the truth to drift from it.
func lineAfter(file io.ReaderAt, size, afterSeq int64) (int64, error) {
	if afterSeq <= 0 {
		return 0, nil
	}
	low, high := int64(0), size
	for low < high {
		middle := low + (high-low)/2
		start, seq, err := lineFrom(file, size, middle)
		if err != nil {
			return 0, err
		}
		if start < size && seq <= afterSeq {
			low = start + 1
		} else {
			high = middle
		}
	}
	start, _, err := lineFrom(file, size, low)
	return start, err
}

// lineFrom finds the first line starting at or after offset and reads its seq.
// start is size when no line starts there.
func lineFrom(file io.ReaderAt, size, offset int64) (start, seq int64, err error) {
	start = offset
	if offset > 0 {
		newline, err := nextNewline(file, size, offset-1)
		if err != nil {
			return 0, 0, err
		}
		start = newline + 1
	}
	if start >= size {
		return size, 0, nil
	}
	seq, err = lineSeq(file, size, start)
	return start, seq, err
}

// nextNewline is the offset of the first newline at or after offset, or size.
func nextNewline(file io.ReaderAt, size, offset int64) (int64, error) {
	buffer := make([]byte, seekChunk)
	for position := offset; position < size; position += seekChunk {
		read, err := file.ReadAt(buffer[:min(seekChunk, size-position)], position)
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, fmt.Errorf("read at %d: %w", position, err)
		}
		if index := bytes.IndexByte(buffer[:read], '\n'); index >= 0 {
			return position + int64(index), nil
		}
	}
	return size, nil
}

// lineSeq reads the seq the line at start was written under.
func lineSeq(file io.ReaderAt, size, start int64) (int64, error) {
	buffer := make([]byte, min(int64(len(linePrefix)+20), size-start))
	if _, err := file.ReadAt(buffer, start); err != nil && !errors.Is(err, io.EOF) {
		return 0, fmt.Errorf("read line at %d: %w", start, err)
	}
	digits, ok := bytes.CutPrefix(buffer, linePrefix)
	end := bytes.IndexByte(digits, ',')
	if !ok || end <= 0 {
		return 0, fmt.Errorf("line at %d does not start with a seq: %q", start, buffer)
	}
	seq, err := strconv.ParseInt(string(digits[:end]), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("line at %d: seq %q: %w", start, digits[:end], err)
	}
	return seq, nil
}
