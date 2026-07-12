package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"sigs.k8s.io/yaml"
)

// ProfileStore persists query Profiles in YAML for standalone CLI use and
// switches to PostgreSQL when UseDB is called by query serve.
type ProfileStore struct {
	Dir string

	mu         sync.RWMutex
	db         *gorm.DB
	registered map[string]struct{}
}

type profileRecord struct {
	ID        uuid.UUID  `gorm:"column:id;primaryKey;default:generate_ulid()"`
	Name      string     `gorm:"column:name"`
	Namespace string     `gorm:"column:namespace"`
	Spec      types.JSON `gorm:"column:spec;type:jsonb"`
	CreatedAt time.Time  `gorm:"column:created_at;default:now();<-:create"`
	UpdatedAt time.Time  `gorm:"column:updated_at;default:now()"`
}

func (profileRecord) TableName() string { return "profiles" }

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
	return &ProfileStore{Dir: abs, registered: map[string]struct{}{}}, nil
}

// List returns all profiles in the store, sorted by name.
func (s *ProfileStore) List() ([]query.Profile, error) {
	if db := s.database(); db != nil {
		var records []profileRecord
		if err := db.Order("name").Find(&records).Error; err != nil {
			return nil, fmt.Errorf("list profiles: %w", err)
		}
		profiles := make([]query.Profile, len(records))
		for i := range records {
			p, err := records[i].profile()
			if err != nil {
				return nil, err
			}
			profiles[i] = p
		}
		return profiles, nil
	}
	return s.listFiles()
}

func (s *ProfileStore) listFiles() ([]query.Profile, error) {
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

// Save upserts the profile in PostgreSQL when configured, otherwise it writes
// <slug>.yaml and overwrites any existing file.
func (s *ProfileStore) Save(p query.Profile) error {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return fmt.Errorf("profile name is required")
	}
	slug := slugify(name)
	if slug == "" {
		return fmt.Errorf("profile name %q has no usable filename", name)
	}

	if db := s.database(); db != nil {
		return saveProfileRecord(db, p, true)
	}
	return s.saveFile(p)
}

func (s *ProfileStore) saveFile(p query.Profile) error {
	name := strings.TrimSpace(p.Name)
	slug := slugify(name)
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

// Delete removes the profile from the active PostgreSQL or YAML store.
func (s *ProfileStore) Delete(name string) error {
	if db := s.database(); db != nil {
		p, err := s.Get(name)
		if err != nil {
			return err
		}
		result := db.Where("name = ?", p.Name).Delete(&profileRecord{})
		if result.Error != nil {
			return fmt.Errorf("delete profile %q: %w", name, result.Error)
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("profile %q not found", name)
		}
		return nil
	}
	slug := slugify(name)
	path := filepath.Join(s.Dir, slug+".yaml")
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete profile %q: %w", name, err)
	}
	return nil
}

// UseDB switches the store to the migrated profiles table. Existing YAML
// profiles are imported only when their name is not already present, making the
// transition durable without overwriting later database edits on restart.
func (s *ProfileStore) UseDB(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("profile database is nil")
	}
	files, err := s.listFiles()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.db = db
	s.mu.Unlock()
	for _, p := range files {
		if err := saveProfileRecord(db, p, false); err != nil {
			return fmt.Errorf("import YAML profile %q: %w", p.Name, err)
		}
	}
	return nil
}

func (s *ProfileStore) database() *gorm.DB {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.db
}

func (s *ProfileStore) markRegistered(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	slug := slugify(name)
	if _, ok := s.registered[slug]; ok {
		return false
	}
	s.registered[slug] = struct{}{}
	return true
}

func saveProfileRecord(db *gorm.DB, p query.Profile, update bool) error {
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal profile %q: %w", p.Name, err)
	}
	record := profileRecord{Name: p.Name, Namespace: p.Namespace, Spec: types.JSON(data)}
	onConflict := clause.OnConflict{Columns: []clause.Column{{Name: "name"}}, DoNothing: true}
	if update {
		onConflict = clause.OnConflict{
			Columns:   []clause.Column{{Name: "name"}},
			DoUpdates: clause.AssignmentColumns([]string{"namespace", "spec", "updated_at"}),
		}
	}
	if err := db.Clauses(onConflict).Create(&record).Error; err != nil {
		return fmt.Errorf("save profile %q: %w", p.Name, err)
	}
	return nil
}

func (r profileRecord) profile() (query.Profile, error) {
	var p query.Profile
	if err := json.Unmarshal(r.Spec, &p); err != nil {
		return query.Profile{}, fmt.Errorf("decode profile %q: %w", r.Name, err)
	}
	// Keep indexed columns authoritative so future spec migrations can reshape
	// the JSON document without changing lookup semantics.
	p.Name = r.Name
	p.Namespace = r.Namespace
	return p, nil
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
