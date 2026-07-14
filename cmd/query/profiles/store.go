package profiles

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"sigs.k8s.io/yaml"
)

type Store interface {
	List(context.Context) ([]query.Profile, error)
	Get(context.Context, string) (query.Profile, error)
	Save(context.Context, query.Profile) error
	Delete(context.Context, string) error
}

type FileStore struct{ Dir string }

type DBStore struct{ db *gorm.DB }

type profileRecord struct {
	ID        uuid.UUID  `gorm:"column:id;primaryKey;default:generate_ulid()"`
	Name      string     `gorm:"column:name"`
	Namespace string     `gorm:"column:namespace"`
	Spec      types.JSON `gorm:"column:spec;type:jsonb"`
	CreatedAt time.Time  `gorm:"column:created_at;default:now();<-:create"`
	UpdatedAt time.Time  `gorm:"column:updated_at;default:now()"`
}

func (profileRecord) TableName() string { return "profiles" }

func NewFileStore(dir string) (*FileStore, error) {
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
	return &FileStore{Dir: abs}, nil
}

func NewDBStore(db *gorm.DB) (*DBStore, error) {
	if db == nil {
		return nil, fmt.Errorf("profile database is required")
	}
	return &DBStore{db: db}, nil
}

func Import(ctx context.Context, source Store, target *DBStore) error {
	if source == nil {
		return fmt.Errorf("profile import source is required")
	}
	if target == nil {
		return fmt.Errorf("profile import target is required")
	}
	items, err := source.List(ctx)
	if err != nil {
		return err
	}
	for _, profile := range items {
		if err := target.save(ctx, profile, false); err != nil {
			return fmt.Errorf("import YAML profile %q: %w", profile.Name, err)
		}
	}
	return nil
}

func (s *FileStore) List(context.Context) ([]query.Profile, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, fmt.Errorf("read profiles dir %q: %w", s.Dir, err)
	}
	profiles := make([]query.Profile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}
		profile, err := s.read(filepath.Join(s.Dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	return profiles, nil
}

func (s *FileStore) Get(ctx context.Context, name string) (query.Profile, error) {
	profiles, err := s.List(ctx)
	if err != nil {
		return query.Profile{}, err
	}
	return findProfile(profiles, name)
}

func (s *FileStore) Save(_ context.Context, profile query.Profile) error {
	name, slug, err := validateProfile(profile)
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(profile)
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

func (s *FileStore) Delete(_ context.Context, name string) error {
	path := filepath.Join(s.Dir, slugify(name)+".yaml")
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete profile %q: %w", name, err)
	}
	return nil
}

func (s *FileStore) read(path string) (query.Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return query.Profile{}, fmt.Errorf("read profile %q: %w", path, err)
	}
	var profile query.Profile
	if err := yaml.Unmarshal(data, &profile); err != nil {
		return query.Profile{}, fmt.Errorf("parse profile %q: %w", path, err)
	}
	if profile.Name == "" {
		return query.Profile{}, fmt.Errorf("profile %q is missing a name", path)
	}
	return profile, nil
}

func (s *DBStore) List(ctx context.Context) ([]query.Profile, error) {
	var records []profileRecord
	if err := s.db.WithContext(ctx).Order("name").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list profiles: %w", err)
	}
	profiles := make([]query.Profile, len(records))
	for i := range records {
		profile, err := records[i].profile()
		if err != nil {
			return nil, err
		}
		profiles[i] = profile
	}
	return profiles, nil
}

func (s *DBStore) Get(ctx context.Context, name string) (query.Profile, error) {
	profiles, err := s.List(ctx)
	if err != nil {
		return query.Profile{}, err
	}
	return findProfile(profiles, name)
}

func (s *DBStore) Save(ctx context.Context, profile query.Profile) error {
	if _, _, err := validateProfile(profile); err != nil {
		return err
	}
	return s.save(ctx, profile, true)
}

func (s *DBStore) Delete(ctx context.Context, name string) error {
	profile, err := s.Get(ctx, name)
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Where("name = ?", profile.Name).Delete(&profileRecord{})
	if result.Error != nil {
		return fmt.Errorf("delete profile %q: %w", name, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("profile %q not found", name)
	}
	return nil
}

func (s *DBStore) save(ctx context.Context, profile query.Profile, update bool) error {
	data, err := json.Marshal(profile)
	if err != nil {
		return fmt.Errorf("marshal profile %q: %w", profile.Name, err)
	}
	record := profileRecord{Name: profile.Name, Namespace: profile.Namespace, Spec: types.JSON(data)}
	onConflict := clause.OnConflict{Columns: []clause.Column{{Name: "name"}}, DoNothing: true}
	if update {
		onConflict = clause.OnConflict{
			Columns:   []clause.Column{{Name: "name"}},
			DoUpdates: clause.AssignmentColumns([]string{"namespace", "spec", "updated_at"}),
		}
	}
	if err := s.db.WithContext(ctx).Clauses(onConflict).Create(&record).Error; err != nil {
		return fmt.Errorf("save profile %q: %w", profile.Name, err)
	}
	return nil
}

func (r profileRecord) profile() (query.Profile, error) {
	var profile query.Profile
	if err := json.Unmarshal(r.Spec, &profile); err != nil {
		return query.Profile{}, fmt.Errorf("decode profile %q: %w", r.Name, err)
	}
	profile.Name = r.Name
	profile.Namespace = r.Namespace
	return profile, nil
}

func findProfile(profiles []query.Profile, name string) (query.Profile, error) {
	for _, profile := range profiles {
		if profile.Name == name {
			return profile, nil
		}
	}
	slug := strings.TrimPrefix(name, "profile-")
	for _, profile := range profiles {
		if slugify(profile.Name) == slug {
			return profile, nil
		}
	}
	return query.Profile{}, fmt.Errorf("profile %q not found", name)
}

func validateProfile(profile query.Profile) (string, string, error) {
	name := strings.TrimSpace(profile.Name)
	if name == "" {
		return "", "", fmt.Errorf("profile name is required")
	}
	slug := slugify(name)
	if slug == "" {
		return "", "", fmt.Errorf("profile name %q has no usable filename", name)
	}
	return name, slug, nil
}

func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

func slugify(name string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == ' ' || r == '-' || r == '_' || r == '/' || r == '.':
			builder.WriteRune('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}
