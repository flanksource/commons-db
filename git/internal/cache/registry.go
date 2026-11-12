package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Binding is <root>/<key>/repository.json: the cache key whose mirror holds
// key's objects, and the (redacted) URL of the repository key caches. Bindings
// written before Repository was recorded leave it empty.
type Binding struct {
	Version    int    `json:"version"`
	Owner      string `json:"owner"`
	Repository string `json:"repository,omitempty"`
}

// ErrOwnerMissing is returned for a binding whose owner no longer has a mirror
// (it was evicted), so the binding must be discovered again.
var ErrOwnerMissing = errors.New("cache owner has no mirror")

// Load reads key's binding; a key without one owns its mirror.
func Load(root, key string) (Binding, error) {
	if key == "." || key != filepath.Base(key) || !filepath.IsLocal(key) {
		return Binding{}, fmt.Errorf("invalid cache key %q", key)
	}
	dir, err := os.OpenRoot(root)
	if errors.Is(err, os.ErrNotExist) {
		return Binding{Version: 1, Owner: key}, nil
	}
	if err != nil {
		return Binding{}, err
	}
	defer dir.Close()
	data, err := dir.ReadFile(filepath.Join(key, "repository.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Binding{Version: 1, Owner: key}, nil
	}
	if err != nil {
		return Binding{}, err
	}
	var binding Binding
	if err := json.Unmarshal(data, &binding); err != nil {
		return Binding{}, fmt.Errorf("cache binding %s: %w", key, err)
	}
	if binding.Version != 1 || binding.Owner == "" || binding.Owner == "." || binding.Owner != filepath.Base(binding.Owner) || !filepath.IsLocal(binding.Owner) {
		return Binding{}, fmt.Errorf("invalid cache binding for %s", key)
	}
	if _, err := dir.Stat(filepath.Join(binding.Owner, "mirror.git", "HEAD")); errors.Is(err, os.ErrNotExist) {
		return binding, fmt.Errorf("binding of %s to %s: %w", key, binding.Owner, ErrOwnerMissing)
	} else if err != nil {
		return Binding{}, fmt.Errorf("cache owner %s: %w", binding.Owner, err)
	}
	return binding, nil
}

// Save atomically writes key's binding.
func Save(root, key string, binding Binding) (err error) {
	dir := filepath.Join(root, key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	binding.Version = 1
	data, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".repository-*")
	if err != nil {
		return err
	}
	defer func() {
		if removeErr := os.Remove(file.Name()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	if _, err = file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, "repository.json"))
}

// Remove deletes key's binding, so its next read discovers an owner again.
func Remove(root, key string) error {
	if err := os.Remove(filepath.Join(root, key, "repository.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
