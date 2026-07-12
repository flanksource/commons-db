package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/commons-db/query"
)

// profileSurfaceParent groups every per-profile dynamic entity under one sidebar
// section. It is the x-clicky-parent of each generated profile entity.
const profileSurfaceParent = "profiles"

// profileItem adapts a query.Profile to clicky's EntityItem. The embedded Profile
// is promoted in JSON, so list/get responses carry the full definition (used by
// the UI to pre-fill the edit form).
type profileItem struct {
	query.Profile
}

func (p profileItem) GetID() string   { return p.Name }
func (p profileItem) GetName() string { return p.Name }

// Columns implements api.TableProvider so the profiles list renders as a clicky
// table of name + provider type (the "connection type") + the referenced
// connection and a truncated query, on both the CLI and the web /profiles
// surface.
func (p profileItem) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("name").Label("Name").Style("font-bold").Build(),
		api.Column("type").Label("Type").Build(),
		api.Column("connection").Label("Connection").Style("text-muted").Build(),
		api.Column("query").Label("Query").MaxWidth(60).Style("text-muted").Build(),
	}
}

// Row implements api.TableProvider, returning the cell values for Columns.
func (p profileItem) Row() map[string]any {
	return map[string]any{
		"name":       p.Name,
		"type":       p.Provider.Type,
		"connection": p.Provider.Connection,
		"query":      p.Query,
	}
}

// profileListOpts are the (currently empty) list options for the profile entity.
type profileListOpts struct{}

// registerProfileEntity registers the YAML-backed profile entity with list +
// full CRUD on the CLI and over HTTP. Create/Update use the context-aware
// handlers so the nested profile body (provider/params/columns) survives via
// rpc.RequestFromContext instead of the executor's flag-flattening. Execution
// (GET /{name}?params) is served by execHandler and schemas by schemaHandler.
func registerProfileEntity(store *ProfileStore) {
	clicky.NewEntity[profileItem, profileListOpts, profileItem]("profiles").
		List(func(profileListOpts) ([]profileItem, error) {
			ps, err := store.List()
			if err != nil {
				return nil, err
			}
			items := make([]profileItem, len(ps))
			for i, p := range ps {
				items[i] = profileItem{p}
			}
			return items, nil
		}).
		Get(func(id string) (profileItem, error) {
			p, err := store.Get(id)
			if err != nil {
				return profileItem{}, err
			}
			return profileItem{p}, nil
		}).
		CreateWithContext(func(ctx context.Context, body map[string]any) (profileItem, error) {
			return saveProfile(store, ctx, body, "")
		}).
		UpdateWithContext(func(ctx context.Context, id string, body map[string]any) (profileItem, error) {
			return saveProfile(store, ctx, body, id)
		}).
		DeleteWithContext(func(_ context.Context, id string) error {
			return store.Delete(id)
		}).
		Register()
}

// saveProfile decodes the (nested) request body into a Profile and persists it.
// On update, a path id supplies the profile name when the body omits it.
func saveProfile(store *ProfileStore, ctx context.Context, body map[string]any, id string) (profileItem, error) {
	b, err := nestedBody(ctx, body)
	if err != nil {
		return profileItem{}, err
	}
	p, err := profileFromBody(b)
	if err != nil && id != "" {
		b["profile"] = id
		p, err = profileFromBody(b)
	}
	if err != nil {
		return profileItem{}, err
	}
	if err := store.Save(p); err != nil {
		return profileItem{}, err
	}
	return profileItem{p}, nil
}

// registerProfileEntities registers one clicky dynamic entity per stored profile
// so each profile appears as its own sidebar surface (with a provider-derived
// icon) and is executable: the entity's List runs the profile and returns its
// rows. Entities are snapshotted at startup; a newly created profile needs a
// restart to appear as its own surface (it still executes via execHandler and
// shows in the aggregate list until then).
func registerProfileEntities(store *ProfileStore) error {
	profiles, err := store.List()
	if err != nil {
		return err
	}
	for _, p := range profiles {
		name := p.Name
		schemaJSON, err := profileEntitySchema(p)
		if err != nil {
			return fmt.Errorf("build entity schema for profile %q: %w", name, err)
		}
		if !store.markRegistered(name) {
			continue
		}
		entity.NewDynamicEntity("profile-"+slugify(name), schemaJSON).
			List(func(_ context.Context, opts map[string]string) ([]map[string]any, error) {
				live, err := store.Get(name) // re-read so edits to the profile reflect
				if err != nil {
					return nil, err
				}
				// The base profile flow needs no database; only postgres/sqlite
				// processors do. currentContext() supplies the DB-backed context
				// under `serve` and a DB-less one on the CLI.
				res, err := query.Execute(currentContext(), live, toParams(opts))
				if err != nil {
					return nil, err
				}
				return res.Rows, nil
			}).
			Register()
	}
	return nil
}

// profileEntitySchema builds the dynamic-entity JSON schema for a profile: its
// visible columns become the entity properties (the first is the id/name key),
// grouped under the profiles surface and tagged with the provider icon. A
// column-less profile gets a synthesized id property so the schema is valid;
// rows still render via the map-backed dynamic item.
func profileEntitySchema(p query.Profile) ([]byte, error) {
	props := map[string]any{}
	idAssigned := false
	for _, c := range p.Columns {
		if c.Hidden {
			continue
		}
		prop := map[string]any{"type": columnJSONType(c.Type)}
		if c.Label != "" {
			prop["x-clicky-label"] = c.Label
		}
		if c.Format != "" {
			prop["x-clicky-format"] = c.Format
		}
		if !idAssigned {
			prop["x-clicky-id"] = true
			prop["x-clicky-name"] = true
			idAssigned = true
		}
		props[c.Name] = prop
	}
	if !idAssigned {
		props["id"] = map[string]any{"type": "string", "x-clicky-id": true, "x-clicky-name": true}
	}
	doc := map[string]any{
		"type":            "object",
		"properties":      props,
		"x-clicky-parent": profileSurfaceParent,
		"x-clicky-icon":   providerIcon(p.Provider.Type),
		"x-clicky-title":  p.Name,
	}
	// x-clicky-render lets the frontend pick a presentation (e.g. the LogsTable
	// view for trace/log profiles) instead of the default data table.
	if p.Render != "" {
		doc["x-clicky-render"] = p.Render
	}
	return json.Marshal(doc)
}

// columnJSONType maps a profile ColumnType to a JSON Schema scalar type.
func columnJSONType(t query.ColumnType) string {
	switch t {
	case query.ColumnTypeNumber:
		return "number"
	case query.ColumnTypeBoolean:
		return "boolean"
	default:
		return "string"
	}
}

// toParams converts the request flag map to the params map query.Execute expects.
func toParams(opts map[string]string) map[string]any {
	params := make(map[string]any, len(opts))
	for k, v := range opts {
		params[k] = v
	}
	return params
}

// profileFromBody decodes a request body into a Profile, failing fast on a
// missing name.
func profileFromBody(body map[string]any) (query.Profile, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return query.Profile{}, fmt.Errorf("encode profile body: %w", err)
	}
	var p query.Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return query.Profile{}, fmt.Errorf("invalid profile: %w", err)
	}
	if p.Name == "" {
		return query.Profile{}, fmt.Errorf("profile name is required")
	}
	return p, nil
}
