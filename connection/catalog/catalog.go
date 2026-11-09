// Package catalog persists organization-owned connections as encrypted payloads.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/secret"
	"github.com/google/uuid"
	"gocloud.dev/secrets"
	"gorm.io/gorm"
)

type Record struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	OrgID     uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:connections_identity"`
	Namespace string    `gorm:"not null;uniqueIndex:connections_identity"`
	Name      string    `gorm:"not null;uniqueIndex:connections_identity"`
	Type      string    `gorm:"not null"`
	KeyID     string    `gorm:"not null"`
	Payload   []byte    `gorm:"not null" json:"-"`
	CreatedBy *uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (Record) TableName() string { return "connections" }

type Options struct {
	DB     *gorm.DB
	OrgID  uuid.UUID
	Keeper *secrets.Keeper
	KeyID  string
}

type Store struct{ options Options }

func New(options Options) (*Store, error) {
	if options.DB == nil || options.OrgID == uuid.Nil || options.Keeper == nil || options.KeyID == "" {
		return nil, fmt.Errorf("connection catalog requires database, organization, keeper and key ID")
	}
	return &Store{options: options}, nil
}

func (s *Store) Writable() bool { return true }

func (s *Store) ListConnections() ([]*models.Connection, error) {
	var rows []Record
	if err := s.options.DB.Where("org_id = ?", s.options.OrgID).Order("namespace, name").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]*models.Connection, 0, len(rows))
	for _, row := range rows {
		c, err := s.open(s.options.DB.Statement.Context, row)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, nil
}

func (s *Store) ResolveConnection(ctx dbcontext.Context, reference string) (*models.Connection, error) {
	query := s.options.DB.WithContext(ctx).Where("org_id = ?", s.options.OrgID)
	if id, err := uuid.Parse(reference); err == nil {
		query = query.Where("id = ?", id)
	} else {
		name := strings.TrimPrefix(reference, "connection://")
		namespace, name, qualified := strings.Cut(name, "/")
		if qualified {
			query = query.Where("namespace = ? AND name = ?", namespace, name)
		} else {
			query = query.Where("name = ?", namespace)
		}
	}
	var rows []Record
	if err := query.Limit(2).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("connection %q: %w", reference, gorm.ErrRecordNotFound)
	}
	if len(rows) > 1 {
		return nil, fmt.Errorf("connection %q is ambiguous; specify namespace/name", reference)
	}
	return s.open(ctx, rows[0])
}

func (s *Store) open(ctx context.Context, row Record) (*models.Connection, error) {
	if row.KeyID != s.options.KeyID {
		return nil, fmt.Errorf("connection %s requires key %s; restore the matching KEK", row.ID, row.KeyID)
	}
	var plain secret.Sensitive
	var err error
	plain, err = s.options.Keeper.Decrypt(ctx, row.Payload)
	if err != nil {
		return nil, fmt.Errorf("decrypt connection %s: %w", row.ID, err)
	}
	var envelope struct {
		OrgID      uuid.UUID
		Connection models.Connection
	}
	if err := json.Unmarshal([]byte(plain), &envelope); err != nil {
		return nil, fmt.Errorf("invalid encrypted connection %s", row.ID)
	}
	c := envelope.Connection
	if envelope.OrgID != s.options.OrgID || c.ID != row.ID || c.Name != row.Name || c.Namespace != row.Namespace || c.Type != row.Type {
		return nil, fmt.Errorf("encrypted connection %s ownership or metadata mismatch", row.ID)
	}
	c.CreatedAt, c.UpdatedAt, c.CreatedBy = row.CreatedAt, row.UpdatedAt, row.CreatedBy
	return &c, nil
}

func (s *Store) SaveConnection(ctx context.Context, c *models.Connection) (*models.Connection, error) {
	if c.Name == "" || c.Type == "" {
		return nil, fmt.Errorf("connection name and type are required")
	}
	copy := *c
	creating := copy.ID == uuid.Nil
	if creating {
		copy.ID = uuid.New()
	} else if _, err := s.ResolveConnection(dbcontext.NewContext(ctx), copy.ID.String()); err != nil {
		return nil, err
	}
	var plain secret.Sensitive
	var err error
	plain, err = json.Marshal(struct {
		OrgID      uuid.UUID
		Connection models.Connection
	}{s.options.OrgID, copy})
	if err != nil {
		return nil, err
	}
	encrypted, err := s.options.Keeper.Encrypt(ctx, []byte(plain))
	if err != nil {
		return nil, fmt.Errorf("encrypt connection %s: %w", copy.ID, err)
	}
	row := Record{ID: copy.ID, OrgID: s.options.OrgID, Name: copy.Name, Namespace: copy.Namespace, Type: copy.Type, KeyID: s.options.KeyID, Payload: encrypted, CreatedBy: copy.CreatedBy}
	db := s.options.DB.WithContext(ctx)
	if creating {
		err = db.Create(&row).Error
	} else {
		result := db.Model(&Record{}).Where("id = ? AND org_id = ?", copy.ID, s.options.OrgID).Updates(map[string]any{"name": row.Name, "namespace": row.Namespace, "type": row.Type, "key_id": row.KeyID, "payload": row.Payload, "updated_at": time.Now()})
		err = result.Error
		if err == nil && result.RowsAffected != 1 {
			err = gorm.ErrRecordNotFound
		}
	}
	if err != nil {
		return nil, fmt.Errorf("save connection: %w", err)
	}
	return s.ResolveConnection(dbcontext.NewContext(ctx), copy.ID.String())
}

func (s *Store) DeleteConnection(ctx context.Context, reference string) error {
	c, err := s.ResolveConnection(dbcontext.NewContext(ctx), reference)
	if err != nil {
		return err
	}
	result := s.options.DB.WithContext(ctx).Where("id = ? AND org_id = ?", c.ID, s.options.OrgID).Delete(&Record{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
