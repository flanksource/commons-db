package main

import (
	"encoding/json"
	"fmt"

	"github.com/flanksource/clicky"
	"github.com/flanksource/commons-db/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// connListOpts are the list/filter options for the connection entity.
type connListOpts struct {
	Type string `flag:"type" help:"Filter by connection type"`
}

// registerConnectionEntity registers the DB-backed connection read entity for
// OpenAPI/CLI discovery. Mutations (create/update/delete) are served by
// writeHandler — clicky's executor can't accept nested JSON bodies (e.g.
// properties) — and the if/then schema is served via content negotiation.
func registerConnectionEntity(db *gorm.DB) {
	clicky.NewEntity[*models.Connection, connListOpts, *models.Connection]("connection").
		List(func(o connListOpts) ([]*models.Connection, error) {
			return listConnections(db, o)
		}).
		Get(func(id string) (*models.Connection, error) {
			c, err := findConnection(db, id)
			if err != nil {
				return nil, err
			}
			redactConnection(c)
			return c, nil
		}).
		Register()
}

// listConnections returns redacted connections, optionally filtered by type.
func listConnections(db *gorm.DB, o connListOpts) ([]*models.Connection, error) {
	q := db.Model(&models.Connection{})
	if o.Type != "" {
		q = q.Where("type = ?", o.Type)
	}
	var conns []*models.Connection
	if err := q.Order("name").Find(&conns).Error; err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	for _, c := range conns {
		redactConnection(c)
	}
	return conns, nil
}

// createConnection inserts a new connection and returns it redacted.
func createConnection(db *gorm.DB, body map[string]any) (*models.Connection, error) {
	c, err := connectionFromBody(body)
	if err != nil {
		return nil, err
	}
	c.ID = uuid.Nil // let the DB assign the id
	if err := db.Create(c).Error; err != nil {
		return nil, fmt.Errorf("create connection: %w", err)
	}
	redactConnection(c)
	return c, nil
}

// updateConnection applies an edit, preserving stored secrets left blank.
func updateConnection(db *gorm.DB, id string, body map[string]any) (*models.Connection, error) {
	existing, err := findConnection(db, id)
	if err != nil {
		return nil, err
	}
	incoming, err := connectionFromBody(body)
	if err != nil {
		return nil, err
	}
	applyConnectionUpdate(existing, incoming)
	if err := db.Save(existing).Error; err != nil {
		return nil, fmt.Errorf("update connection: %w", err)
	}
	redactConnection(existing)
	return existing, nil
}

// deleteConnection removes a connection by id, erroring when absent.
func deleteConnection(db *gorm.DB, id string) error {
	res := db.Where("id = ?", id).Delete(&models.Connection{})
	if res.Error != nil {
		return fmt.Errorf("delete connection: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("connection %q not found", id)
	}
	return nil
}

// findConnection resolves a connection by UUID, falling back to its name.
func findConnection(db *gorm.DB, id string) (*models.Connection, error) {
	var c models.Connection
	q := db.Model(&models.Connection{})
	if _, err := uuid.Parse(id); err == nil {
		q = q.Where("id = ?", id)
	} else {
		q = q.Where("name = ?", id)
	}
	if err := q.First(&c).Error; err != nil {
		return nil, fmt.Errorf("connection %q not found: %w", id, err)
	}
	return &c, nil
}

// connectionFromBody decodes a request body into a Connection, failing fast on a
// missing name or type.
func connectionFromBody(body map[string]any) (*models.Connection, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode connection body: %w", err)
	}
	var c models.Connection
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("invalid connection: %w", err)
	}
	if c.Name == "" {
		return nil, fmt.Errorf("connection name is required")
	}
	if c.Type == "" {
		return nil, fmt.Errorf("connection type is required")
	}
	return &c, nil
}

// applyConnectionUpdate copies editable fields from incoming onto existing,
// preserving stored secrets when the incoming payload leaves them blank.
func applyConnectionUpdate(existing, incoming *models.Connection) {
	existing.Name = incoming.Name
	existing.Namespace = incoming.Namespace
	existing.Type = incoming.Type
	existing.URL = incoming.URL
	existing.Username = incoming.Username
	existing.Properties = incoming.Properties
	existing.InsecureTLS = incoming.InsecureTLS
	if incoming.Password != "" {
		existing.Password = incoming.Password
	}
	if incoming.Certificate != "" {
		existing.Certificate = incoming.Certificate
	}
}

// redactConnection blanks secrets so they are never returned over the API.
func redactConnection(c *models.Connection) {
	if c == nil {
		return
	}
	c.Password = ""
	c.Certificate = ""
}
