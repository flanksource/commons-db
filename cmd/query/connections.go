package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/commons-db/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// connListOpts are the list/filter options for the connection entity. Types is
// the comma-separated form used by the connection lookup to scope a picker to the
// connection types valid for a profile's provider (e.g. sql → postgres,mysql,...).
type connListOpts struct {
	Type  string `flag:"type" help:"Filter by connection type"`
	Types string `flag:"types" help:"Filter by connection types (comma-separated)"`
}

// registerConnectionEntity registers the DB-backed connection entity with full
// CRUD on both the CLI and over HTTP. Create/Update use the context-aware
// handlers so they can read the raw nested JSON body (e.g. connection
// `properties`) clicky stashes on the context via rpc.RequestFromContext, which
// the executor would otherwise flatten to string flags. The DB is resolved
// lazily (currentDB) because entities register before the database exists.
func registerConnectionEntity() {
	clicky.NewEntity[*models.Connection, connListOpts, *models.Connection]("connection").
		List(func(o connListOpts) ([]*models.Connection, error) {
			db, err := currentDB()
			if err != nil {
				return nil, err
			}
			return listConnections(db, o)
		}).
		Get(func(id string) (*models.Connection, error) {
			db, err := currentDB()
			if err != nil {
				return nil, err
			}
			c, err := findConnection(db, id)
			if err != nil {
				return nil, err
			}
			redactConnection(c)
			return c, nil
		}).
		CreateWithContext(func(ctx context.Context, body map[string]any) (*models.Connection, error) {
			db, err := currentDB()
			if err != nil {
				return nil, err
			}
			b, err := nestedBody(ctx, body)
			if err != nil {
				return nil, err
			}
			return createConnection(db, b)
		}).
		UpdateWithContext(func(ctx context.Context, id string, body map[string]any) (*models.Connection, error) {
			db, err := currentDB()
			if err != nil {
				return nil, err
			}
			b, err := nestedBody(ctx, body)
			if err != nil {
				return nil, err
			}
			return updateConnection(db, id, b)
		}).
		DeleteWithContext(func(_ context.Context, id string) error {
			db, err := currentDB()
			if err != nil {
				return err
			}
			return deleteConnection(db, id)
		}).
		Filters(connectionFilter{}).
		Register()
}

// listConnections returns redacted connections, optionally filtered by type(s).
func listConnections(db *gorm.DB, o connListOpts) ([]*models.Connection, error) {
	q := db.Model(&models.Connection{})
	if types := connectionTypeFilter(o); len(types) > 0 {
		q = q.Where("type IN ?", types)
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

// connectionTypeFilter collects the type filter from the single `type` flag and
// the comma-separated `types` scope used by the lookup, de-duplicating blanks.
func connectionTypeFilter(o connListOpts) []string {
	var types []string
	if o.Type != "" {
		types = append(types, o.Type)
	}
	for _, t := range strings.Split(o.Types, ",") {
		if t = strings.TrimSpace(t); t != "" {
			types = append(types, t)
		}
	}
	return types
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
// missing name or type. The body's id is dropped: create lets the DB assign one
// and update addresses the row by path/flag id, so a body id (which may be a name
// on update) must never be parsed into the uuid ID field.
func connectionFromBody(body map[string]any) (*models.Connection, error) {
	delete(body, "id")
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
	if isHTTPAuthConnectionType(c.Type) {
		c.Properties = maps.Clone(c.Properties)
		if c.Properties == nil {
			c.Properties = map[string]string{}
		}
		if c.Properties["authType"] == "" {
			c.Properties["authType"] = inferHTTPAuthType(c)
		}
	}
	c.Password = ""
	c.Certificate = ""
}

func isHTTPAuthConnectionType(connectionType string) bool {
	switch connectionType {
	case models.ConnectionTypeHTTP, models.ConnectionTypeOpenSearch, models.ConnectionTypePrometheus,
		models.ConnectionTypeLoki, models.ConnectionTypeJaeger:
		return true
	default:
		return false
	}
}

func inferHTTPAuthType(c *models.Connection) string {
	if c.Properties["authType"] != "" {
		return c.Properties["authType"]
	}
	if c.Properties["cert"] != "" || c.Properties["key"] != "" {
		return "mtls"
	}
	if c.Properties["clientID"] != "" || c.Properties["clientSecret"] != "" || c.Properties["tokenURL"] != "" {
		return "oauth"
	}
	if c.Username != "" || c.Password != "" || c.Properties["username"] != "" || c.Properties["password"] != "" {
		return "basic"
	}
	return "none"
}
