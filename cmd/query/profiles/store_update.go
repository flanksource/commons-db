package profiles

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"sigs.k8s.io/yaml"
)

const ProfileNameConflictCode = "PROFILE_NAME_CONFLICT"

type UpdateOptions struct {
	ReplaceExisting bool
}

type ProfileNameConflictError struct {
	Source string
	Target string
}

func (e ProfileNameConflictError) Error() string {
	return fmt.Sprintf("%s: profile %q conflicts with existing profile %q", ProfileNameConflictCode, e.Source, e.Target)
}

func (s *FileStore) Update(ctx context.Context, originalName string, profile query.Profile, options UpdateOptions) error {
	profiles, source, target, err := prepareProfileUpdate(ctx, s, originalName, profile, options)
	if err != nil {
		return err
	}
	writes := map[string][]byte{}
	deletes := map[string]struct{}{}
	for _, updated := range profiles {
		if updated.Name != profile.Name && !importsChanged(updated, source.Name, profile.Name) {
			continue
		}
		data, err := yaml.Marshal(updated)
		if err != nil {
			return fmt.Errorf("marshal profile %q: %w", updated.Name, err)
		}
		writes[filepath.Join(s.Dir, slugify(updated.Name)+".yaml")] = data
	}
	if slugify(source.Name) != slugify(profile.Name) {
		deletes[filepath.Join(s.Dir, slugify(source.Name)+".yaml")] = struct{}{}
	}
	if target != nil {
		deletes[filepath.Join(s.Dir, slugify(target.Name)+".yaml")] = struct{}{}
	}
	for path := range writes {
		delete(deletes, path)
	}
	return commitProfileFiles(writes, deletes)
}

func (s *DBStore) Update(ctx context.Context, originalName string, profile query.Profile, options UpdateOptions) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		store := &DBStore{db: tx}
		profiles, source, target, err := prepareProfileUpdate(ctx, store, originalName, profile, options)
		if err != nil {
			return err
		}
		if target != nil {
			if err := tx.Where("name = ?", target.Name).Delete(&profileRecord{}).Error; err != nil {
				return fmt.Errorf("replace profile %q: %w", target.Name, err)
			}
		}
		for _, updated := range profiles {
			if updated.Name != profile.Name && !importsChanged(updated, source.Name, profile.Name) {
				continue
			}
			data, err := json.Marshal(updated)
			if err != nil {
				return fmt.Errorf("marshal profile %q: %w", updated.Name, err)
			}
			original := updated.Name
			if updated.Name == profile.Name {
				original = source.Name
			}
			result := tx.Model(&profileRecord{}).Where("name = ?", original).Updates(map[string]any{
				"name": updated.Name, "namespace": updated.Namespace,
				"spec": types.JSON(data), "updated_at": time.Now(),
			})
			if result.Error != nil {
				return fmt.Errorf("update profile %q: %w", original, result.Error)
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("update profile %q: expected one row, changed %d", original, result.RowsAffected)
			}
		}
		return nil
	})
}

func prepareProfileUpdate(
	ctx context.Context,
	store Store,
	originalName string,
	profile query.Profile,
	options UpdateOptions,
) ([]query.Profile, query.Profile, *query.Profile, error) {
	name, _, err := validateProfile(profile)
	if err != nil {
		return nil, query.Profile{}, nil, err
	}
	profile.Name = name
	profiles, err := store.List(ctx)
	if err != nil {
		return nil, query.Profile{}, nil, err
	}
	source, err := findProfile(profiles, originalName)
	if err != nil {
		return nil, query.Profile{}, nil, err
	}
	var target *query.Profile
	for i := range profiles {
		candidate := profiles[i]
		if candidate.Name == source.Name {
			continue
		}
		if candidate.Name == profile.Name || slugify(candidate.Name) == slugify(profile.Name) {
			target = &candidate
			break
		}
	}
	if target != nil && !options.ReplaceExisting {
		return nil, query.Profile{}, nil, ProfileNameConflictError{Source: source.Name, Target: target.Name}
	}
	updated := make([]query.Profile, 0, len(profiles))
	for _, current := range profiles {
		if current.Name == source.Name || target != nil && current.Name == target.Name {
			continue
		}
		current.Imports = rewriteProfileImports(current.Imports, source.Name, profile.Name)
		updated = append(updated, current)
	}
	profile.Imports = rewriteProfileImports(profile.Imports, source.Name, profile.Name)
	updated = append(updated, profile)
	return updated, source, target, nil
}

func rewriteProfileImports(imports []string, oldName, newName string) []string {
	if oldName == newName {
		return imports
	}
	result := make([]string, 0, len(imports))
	for _, name := range imports {
		if name == oldName {
			name = newName
		}
		if !slices.Contains(result, name) {
			result = append(result, name)
		}
	}
	return result
}

func importsChanged(profile query.Profile, oldName, newName string) bool {
	return oldName != newName && slices.Contains(profile.Imports, newName)
}

func commitProfileFiles(writes map[string][]byte, deletes map[string]struct{}) error {
	token := uuid.NewString()
	temps := map[string]string{}
	for path, data := range writes {
		temp := filepath.Join(filepath.Dir(path), ".profile-update-"+token+"-"+filepath.Base(path))
		if err := os.WriteFile(temp, data, 0o600); err != nil {
			cleanupProfileFiles(temps)
			return fmt.Errorf("stage profile %q: %w", path, err)
		}
		temps[path] = temp
	}
	plannedBackups := map[string]string{}
	for path := range deletes {
		plannedBackups[path] = path + ".backup-" + token
	}
	for path := range writes {
		plannedBackups[path] = path + ".backup-" + token
	}
	movedBackups := map[string]string{}
	for path, backup := range plannedBackups {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			cleanupProfileFiles(temps)
			return fmt.Errorf("inspect profile %q: %w", path, err)
		}
		if err := os.Rename(path, backup); err != nil {
			rollbackProfileFiles(movedBackups, nil, temps)
			return fmt.Errorf("backup profile %q: %w", path, err)
		}
		movedBackups[path] = backup
	}
	committed := map[string]struct{}{}
	for path, temp := range temps {
		if err := os.Rename(temp, path); err != nil {
			rollbackProfileFiles(movedBackups, committed, temps)
			return fmt.Errorf("commit profile %q: %w", path, err)
		}
		committed[path] = struct{}{}
	}
	cleanupProfileFiles(movedBackups)
	return nil
}

func rollbackProfileFiles(backups map[string]string, committed map[string]struct{}, temps map[string]string) {
	for path := range committed {
		_ = os.Remove(path)
	}
	for path, backup := range backups {
		_ = os.Rename(backup, path)
	}
	cleanupProfileFiles(temps)
}

func cleanupProfileFiles(files map[string]string) {
	for _, path := range files {
		_ = os.Remove(path)
	}
}
