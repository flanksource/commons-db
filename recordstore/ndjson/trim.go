package ndjson

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/flanksource/commons-db/recordstore"
)

// Trim removes the rows appended before before.
func (b *Backend) Trim(_ context.Context, stream string, before time.Time) (recordstore.Meta, error) {
	if err := recordstore.ValidateStream(stream); err != nil {
		return recordstore.Meta{}, err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	state, err := b.find(stream)
	if err != nil {
		return recordstore.Meta{}, err
	}
	state, err = b.trimLocked(state, before)
	return state.Meta, err
}

// trimLocked copies the rows it keeps to a new data file, commits the sidecar
// naming it, and only then removes the old file: an interruption leaves either
// the old stream with an unreferenced new file or the new stream with an
// unreferenced old one, and remove deletes both kinds.
func (b *Backend) trimLocked(state sidecar, before time.Time) (sidecar, error) {
	cut := state.LowSeq
	for _, mark := range state.Appends {
		if mark.At.Before(before) {
			cut = mark.Last + 1
		}
	}
	if cut <= state.LowSeq {
		return state, nil
	}
	previous := b.dataPath(state)
	trimmed, err := b.copyFrom(state, cut)
	if err != nil {
		return sidecar{}, err
	}
	if err := b.writeSidecar(trimmed); err != nil {
		return sidecar{}, err
	}
	if err := removeFile(previous); err != nil {
		return sidecar{}, fmt.Errorf("stream %q: %w", state.Stream, err)
	}
	return trimmed, nil
}

// copyFrom writes state's committed lines from seq cut on to the data file a
// stream starting at cut is kept in, and returns the sidecar describing it.
func (b *Backend) copyFrom(state sidecar, cut int64) (sidecar, error) {
	path := b.dataPath(state)
	source, err := os.Open(path)
	if err != nil {
		return sidecar{}, fmt.Errorf("stream %q: open %s: %w", state.Stream, path, err)
	}
	defer func() { _ = source.Close() }()
	offset, err := lineAfter(source, state.Bytes, cut-1)
	if err != nil {
		return sidecar{}, fmt.Errorf("stream %q: find seq %d in %s: %w", state.Stream, cut, path, err)
	}
	trimmed := state
	trimmed.File = state.Stream + trimmedSeparator + strconv.FormatInt(cut, 10) + dataSuffix
	trimmed.Bytes = state.Bytes - offset
	target := b.dataPath(trimmed)
	file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return sidecar{}, fmt.Errorf("stream %q: create %s: %w", state.Stream, target, err)
	}
	_, copyErr := io.Copy(file, io.NewSectionReader(source, offset, trimmed.Bytes))
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return sidecar{}, fmt.Errorf("stream %q: write %s: %w", state.Stream, filepath.Base(target), err)
	}
	trimmed.LowSeq = cut
	trimmed.Total = trimmed.HighSeq - cut + 1
	trimmed.Appends = nil
	for _, mark := range state.Appends {
		if mark.Last >= cut {
			trimmed.Appends = append(trimmed.Appends, mark)
		}
	}
	trimmed.Keys = make(map[string]int64, len(state.Keys))
	for key, seq := range state.Keys {
		if seq >= cut {
			trimmed.Keys[key] = seq
		}
	}
	return trimmed, nil
}
