package readcache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type entry struct {
	Value   []byte
	Expires time.Time
}

type Memory struct {
	mu      sync.Mutex
	entries map[string]entry
}

const maxMemoryEntries = 4096

func (m *Memory) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.entries[key]
	if ok && !time.Now().Before(v.Expires) {
		delete(m.entries, key)
		ok = false
	}
	return bytes.Clone(v.Value), ok, nil
}

func (m *Memory) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = map[string]entry{}
	}
	for name, entry := range m.entries {
		if !time.Now().Before(entry.Expires) {
			delete(m.entries, name)
		}
	}
	if len(m.entries) >= maxMemoryEntries {
		oldest := key
		var expires time.Time
		for name, entry := range m.entries {
			if expires.IsZero() || entry.Expires.Before(expires) {
				oldest, expires = name, entry.Expires
			}
		}
		delete(m.entries, oldest)
	}
	m.entries[key] = entry{Value: bytes.Clone(value), Expires: time.Now().Add(ttl)}
	return nil
}

func (m *Memory) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, key)
	return nil
}

type Filesystem struct{ Root string }

func (s Filesystem) path(key string) (string, error) {
	if s.Root == "" {
		return "", errors.New("read cache directory is required")
	}
	return filepath.Join(s.Root, fmt.Sprintf("%x.json", sha256.Sum256([]byte(key)))), nil
}

func (s Filesystem) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	name, err := s.path(key)
	if err != nil {
		return nil, false, err
	}
	raw, err := os.ReadFile(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var v entry
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false, fmt.Errorf("decode %s: %w", name, err)
	}
	if v.Expires.IsZero() {
		return nil, false, fmt.Errorf("read cache %s has no expiry", name)
	}
	if !time.Now().Before(v.Expires) {
		return nil, false, s.Delete(ctx, key)
	}
	return v.Value, true, ctx.Err()
}

func (s Filesystem) Set(ctx context.Context, key string, value []byte, ttl time.Duration) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	name, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(entry{Value: value, Expires: time.Now().Add(ttl)})
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(s.Root, ".write-")
	if err != nil {
		return err
	}
	defer func() {
		if removeErr := os.Remove(file.Name()); !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	_, writeErr := file.Write(raw)
	if err := errors.Join(writeErr, file.Close(), ctx.Err()); err != nil {
		return err
	}
	return os.Rename(file.Name(), name)
}

func (s Filesystem) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name, err := s.path(key)
	if err != nil {
		return err
	}
	err = os.Remove(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
