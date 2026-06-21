package main

import (
	"encoding/json"
	"fmt"

	"github.com/flanksource/clicky"
	"github.com/flanksource/commons-db/query"
)

// profileItem adapts a query.Profile to clicky's EntityItem. The embedded Profile
// is promoted in JSON, so list/get responses carry the full definition (used by
// the UI to pre-fill the edit form).
type profileItem struct {
	query.Profile
}

func (p profileItem) GetID() string   { return p.Name }
func (p profileItem) GetName() string { return p.Name }

// profileListOpts are the (currently empty) list options for the profile entity.
type profileListOpts struct{}

// registerProfileEntity registers the YAML-backed profile read entity for
// OpenAPI/CLI discovery. Mutations are served by writeHandler (nested JSON
// bodies), execution (GET /{name}?params) by execHandler, and schemas by
// schemaHandler — none go through this entity.
func registerProfileEntity(store *ProfileStore) {
	clicky.NewEntity[profileItem, profileListOpts, profileItem]("profile").
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
		Register()
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
