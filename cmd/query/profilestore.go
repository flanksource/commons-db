package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flanksource/commons-db/query"
	"sigs.k8s.io/yaml"
)

// ProfileStore persists query Profiles as one YAML file per profile under Dir.
// The filename is the profile's slug; lookups match on the profile's Name.
type ProfileStore struct {
	Dir string
}

// NewProfileStore opens (creating if needed) a profile store rooted at dir. The
// directory is resolved to an absolute path so the load location is unambiguous
// in logs and errors regardless of the working directory.
func NewProfileStore(dir string) (*ProfileStore, error) {
	if dir == "" {
		return nil, fmt.Errorf("profiles dir is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve profiles dir %q: %w", dir, err)
	}
	if err := ensurePrivateDir(abs); err != nil {
		return nil, fmt.Errorf("create profiles dir %q: %w", abs, err)
	}
	return &ProfileStore{Dir: abs}, nil
}

// List returns all profiles in the store, sorted by name.
func (s *ProfileStore) List() ([]query.Profile, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, fmt.Errorf("read profiles dir %q: %w", s.Dir, err)
	}

	var profiles []query.Profile
	for _, e := range entries {
		if e.IsDir() || !isYAML(e.Name()) {
			continue
		}
		p, err := s.read(filepath.Join(s.Dir, e.Name()))
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	return profiles, nil
}

// Get returns the profile with the given name.
func (s *ProfileStore) Get(name string) (query.Profile, error) {
	profiles, err := s.List()
	if err != nil {
		return query.Profile{}, err
	}
	for _, p := range profiles {
		if p.Name == name {
			return p, nil
		}
	}
	slug := strings.TrimPrefix(name, "profile-")
	for _, p := range profiles {
		if slugify(p.Name) == slug {
			return p, nil
		}
	}
	return query.Profile{}, fmt.Errorf("profile %q not found", name)
}

// Save writes the profile to <slug>.yaml, overwriting any existing file.
func (s *ProfileStore) Save(p query.Profile) error {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return fmt.Errorf("profile name is required")
	}
	slug := slugify(name)
	if slug == "" {
		return fmt.Errorf("profile name %q has no usable filename", name)
	}

	data, err := yaml.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal profile %q: %w", name, err)
	}
	path := filepath.Join(s.Dir, slug+".yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write profile %q: %w", name, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure profile %q: %w", name, err)
	}
	return nil
}

// Delete removes the profile's YAML file.
func (s *ProfileStore) Delete(name string) error {
	slug := slugify(name)
	path := filepath.Join(s.Dir, slug+".yaml")
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete profile %q: %w", name, err)
	}
	return nil
}

func (s *ProfileStore) read(path string) (query.Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return query.Profile{}, fmt.Errorf("read profile %q: %w", path, err)
	}
	var p query.Profile
	if err := yaml.Unmarshal(data, &p); err != nil {
		return query.Profile{}, fmt.Errorf("parse profile %q: %w", path, err)
	}
	if p.Name == "" {
		return query.Profile{}, fmt.Errorf("profile %q is missing a name", path)
	}
	return p, nil
}

func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

// slugify produces a filesystem-safe slug from a profile name.
func slugify(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_' || r == '/' || r == '.':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
