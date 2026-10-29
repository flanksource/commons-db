package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type Binding struct {
	Version int    `json:"version"`
	Owner   string `json:"owner"`
}

func Load(root, key string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, key, "repository.json"))
	if errors.Is(err, os.ErrNotExist) {
		return key, nil
	}
	if err != nil {
		return "", err
	}
	var binding Binding
	if err := json.Unmarshal(data, &binding); err != nil {
		return "", fmt.Errorf("cache binding %s: %w", key, err)
	}
	if binding.Version != 1 || binding.Owner == "" || binding.Owner == "." || binding.Owner != filepath.Base(binding.Owner) || !filepath.IsLocal(binding.Owner) {
		return "", fmt.Errorf("invalid cache binding for %s", key)
	}
	if _, err := os.Stat(filepath.Join(root, binding.Owner, "mirror.git", "HEAD")); err != nil {
		return "", fmt.Errorf("cache owner %s: %w", binding.Owner, err)
	}
	return binding.Owner, nil
}

func Save(root, key, owner string) (err error) {
	dir := filepath.Join(root, key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(Binding{Version: 1, Owner: owner})
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
	if err = os.Rename(file.Name(), filepath.Join(dir, "repository.json")); err != nil {
		return err
	}
	return nil
}
