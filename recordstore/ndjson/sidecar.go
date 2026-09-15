package ndjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/flanksource/commons-db/recordstore"
)

// find reads stream's sidecar from whichever kind directory holds it. A stream
// past its expiry is removed and reads as not found.
func (b *Backend) find(stream string) (sidecar, error) {
	kinds, err := os.ReadDir(b.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return sidecar{}, fmt.Errorf("stream %q: %w", stream, recordstore.ErrNotFound)
	}
	if err != nil {
		return sidecar{}, fmt.Errorf("stream %q: list %s: %w", stream, b.dir, err)
	}
	var found []string
	for _, kind := range kinds {
		if !kind.IsDir() {
			continue
		}
		if metaPath := b.metaPath(kind.Name(), stream); fileExists(metaPath) {
			found = append(found, metaPath)
		}
	}
	switch len(found) {
	case 0:
		return sidecar{}, fmt.Errorf("stream %q: %w", stream, recordstore.ErrNotFound)
	case 1:
	default:
		return sidecar{}, fmt.Errorf("stream %q is stored under more than one kind: %s", stream, strings.Join(found, ", "))
	}
	state, err := readSidecar(found[0])
	if err != nil {
		return sidecar{}, err
	}
	if state.Expired(b.now()) {
		if err := b.remove(state); err != nil {
			return sidecar{}, err
		}
		return sidecar{}, fmt.Errorf("stream %q expired at %s: %w", stream, state.ExpiresAt, recordstore.ErrNotFound)
	}
	return state, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readSidecar(path string) (sidecar, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return sidecar{}, fmt.Errorf("read %s: %w", path, err)
	}
	var state sidecar
	if err := json.Unmarshal(content, &state); err != nil {
		return sidecar{}, fmt.Errorf("decode %s: %w", path, err)
	}
	if err := state.Validate(); err != nil {
		return sidecar{}, fmt.Errorf("decode %s: %w", path, err)
	}
	if !ownsDataFile(state.Stream, state.File) {
		return sidecar{}, fmt.Errorf("decode %s: data file %q is not one of stream %q's", path, state.File, state.Stream)
	}
	return state, nil
}

// ownsDataFile reports whether name is a data file of stream: <stream>.ndjson,
// or <stream>@<low seq>.ndjson once it was trimmed. A stream id never holds an
// @, so no other stream's file can match.
func ownsDataFile(stream, name string) bool {
	if name == stream+dataSuffix {
		return true
	}
	seq, prefixed := strings.CutPrefix(name, stream+trimmedSeparator)
	seq, suffixed := strings.CutSuffix(seq, dataSuffix)
	if !prefixed || !suffixed || seq == "" {
		return false
	}
	_, err := strconv.ParseUint(seq, 10, 63)
	return err == nil
}

// writeSidecar replaces the sidecar through a rename, so a reader sees the old
// metadata or the new, never half of either.
func (b *Backend) writeSidecar(state sidecar) error {
	path := b.metaPath(state.Kind, state.Stream)
	content, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("stream %q: encode metadata: %w", state.Stream, err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, content, 0o600); err != nil {
		return fmt.Errorf("stream %q: write %s: %w", state.Stream, temporary, err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("stream %q: replace %s: %w", state.Stream, path, err)
	}
	return nil
}

// remove deletes the stream's sidecar and every data file of it: the one the
// sidecar names, and any a trim interrupted before or after its commit left.
func (b *Backend) remove(state sidecar) error {
	if err := os.Remove(b.metaPath(state.Kind, state.Stream)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stream %q: remove its metadata: %w", state.Stream, err)
	}
	dir := filepath.Join(b.dir, state.Kind)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("stream %q: list %s: %w", state.Stream, dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !ownsDataFile(state.Stream, entry.Name()) {
			continue
		}
		if err := removeFile(filepath.Join(dir, entry.Name())); err != nil {
			return fmt.Errorf("stream %q: %w", state.Stream, err)
		}
	}
	return nil
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// rotate makes room for one more stream of kind: it removes every expired
// stream, then the least recently written beyond the budget. A stream being
// written right now is skipped, not waited for.
func (b *Backend) rotate(kind, opening string) error {
	states, err := b.kindStreams(kind)
	if err != nil {
		return err
	}
	sort.Slice(states, func(i, j int) bool { return states[i].UpdatedAt.After(states[j].UpdatedAt) })
	now := b.now()
	for index, state := range states {
		if state.Stream == opening || (index < b.keep-1 && !state.Expired(now)) {
			continue
		}
		unlock, ok := b.locks.TryLock(state.Stream)
		if !ok {
			continue
		}
		err := b.remove(state)
		unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) kindStreams(kind string) ([]sidecar, error) {
	dir := filepath.Join(b.dir, kind)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}
	var states []sidecar
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), metaSuffix) {
			continue
		}
		state, err := readSidecar(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, nil
}
