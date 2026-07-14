package main

import (
	"testing"

	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/commons-db/query"
)

func TestMergeStoredProfilesTracksStoreImmediately(t *testing.T) {
	store, err := NewProfileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile := sampleProfile("Live Sales")
	profile.Provider.Type = "postgres"
	profile.Params = []query.ParamDef{{Name: "region", Type: query.ParamTypeEnum, Options: []string{"US", "EU"}, Required: true}}
	profile.Columns = []query.ColumnDef{{Name: "id", Label: "ID", Type: query.ColumnTypeString, Kind: query.ColumnKindTimestamp}}
	if err := store.Save(profile); err != nil {
		t.Fatal(err)
	}

	spec := &rpc.OpenAPISpec{
		Paths: map[string]rpc.OpenAPIPath{
			"/api/v1/profile-stale": {"get": {Clicky: &rpc.ClickyOperationMeta{Surface: "profile-stale"}}},
		},
		Clicky: &rpc.ClickySpecMeta{Surfaces: []rpc.ClickySurface{
			{Key: "profiles", Entity: "profiles", Title: "Profiles"},
			{Key: "profile-stale", Entity: "profile-stale", Parent: profileSurfaceParent},
		}},
	}
	if err := mergeStoredProfiles(spec, store); err != nil {
		t.Fatal(err)
	}
	if _, exists := spec.Paths["/api/v1/profile-stale"]; exists {
		t.Fatal("startup-snapshotted profile path was not removed")
	}
	op, exists := spec.Paths["/api/v1/profile/profile-live-sales"]["get"]
	if !exists {
		t.Fatalf("live profile operation missing: %+v", spec.Paths)
	}
	if op.Clicky == nil || op.Clicky.Surface != "profile-live-sales" || op.Clicky.Scope != "collection" {
		t.Fatalf("unexpected clicky metadata: %+v", op.Clicky)
	}
	if len(op.Parameters) != 3 || !op.Parameters[0].Required || op.Parameters[0].Clicky.Role != "filter" {
		t.Fatalf("profile filter parameter missing: %+v", op.Parameters)
	}
	if op.Parameters[1].Clicky.Role != "limit" || op.Parameters[2].Clicky.Role != "offset" {
		t.Fatalf("profile pagination parameters missing: %+v", op.Parameters)
	}
	if got := op.Parameters[0].Schema.Enum; len(got) != 2 || got[0] != "US" {
		t.Fatalf("profile enum missing: %+v", got)
	}
	item := op.Responses["200"].Content["application/json"].Schema.Items
	if item.Properties["id"].Extensions["x-clicky-label"] != "ID" {
		t.Fatalf("response column metadata missing: %+v", item.Properties["id"])
	}
	if item.Properties["id"].Extensions["x-clicky-kind"] != "timestamp" {
		t.Fatalf("response timestamp metadata missing: %+v", item.Properties["id"])
	}
	if op.Clicky.Export == nil || len(op.Clicky.Export.Formats) != 8 || len(op.Clicky.Export.Scopes) != 2 || op.Clicky.Export.AllRowsMode != "streaming" || op.Clicky.Export.FormatMaxRows["pdf"] != 1000 {
		t.Fatalf("profile export metadata missing: %+v", op.Clicky.Export)
	}

	if err := store.Delete(profile.Name); err != nil {
		t.Fatal(err)
	}
	if err := mergeStoredProfiles(spec, store); err != nil {
		t.Fatal(err)
	}
	if _, exists := spec.Paths["/api/v1/profile/profile-live-sales"]; exists {
		t.Fatal("deleted profile remained in dynamic OpenAPI")
	}
}

func TestProfileOpenAPIPreservesMappedPagingAndTimeRoles(t *testing.T) {
	spec := &rpc.OpenAPISpec{Paths: map[string]rpc.OpenAPIPath{}, Clicky: &rpc.ClickySpecMeta{}}
	addProfileToSpec(spec, query.Profile{
		Name: "mapped",
		Params: []query.ParamDef{
			{Name: "page_size", Type: query.ParamTypeNumber, Role: query.ParamRoleLimit},
			{Name: "skip", Type: query.ParamTypeNumber, Role: query.ParamRoleOffset},
			{Name: "from", Type: query.ParamTypeDate, Role: query.ParamRoleTimeFrom},
			{Name: "to", Type: query.ParamTypeDate, Role: query.ParamRoleTimeTo},
		},
	})
	op := spec.Paths["/api/v1/profile/profile-mapped"]["get"]
	if len(op.Parameters) != 4 {
		t.Fatalf("mapped parameters should replace built-in pager names: %+v", op.Parameters)
	}
	wantRoles := []string{"limit", "offset", "time-from", "time-to"}
	for i, role := range wantRoles {
		if op.Parameters[i].Clicky == nil || op.Parameters[i].Clicky.Role != role {
			t.Fatalf("parameter %d role = %+v, want %q", i, op.Parameters[i].Clicky, role)
		}
	}
}

func TestProfileOpenAPIAdvertisesStructuredColumnShapes(t *testing.T) {
	schema := profileResponseSchema(query.Profile{Columns: []query.ColumnDef{
		{Name: "labels", Type: query.ColumnTypeKeyValue},
		{Name: "pairs", Type: query.ColumnTypeKeyValues},
		{Name: "metadata", Type: query.ColumnTypeJSON},
	}}).Items

	labels := schema.Properties["labels"]
	if labels.Type != "object" || labels.AdditionalProperties == nil || labels.Extensions["x-clicky-type"] != "key_value" {
		t.Fatalf("labels schema = %#v", labels)
	}
	pairs := schema.Properties["pairs"]
	if pairs.Type != "array" || pairs.Items == nil || pairs.Items.Properties["key"].Type != "string" {
		t.Fatalf("pairs schema = %#v", pairs)
	}
	metadata := schema.Properties["metadata"]
	if len(metadata.OneOf) != 5 || !metadata.Nullable || metadata.Extensions["x-clicky-type"] != "json" {
		t.Fatalf("metadata schema = %#v", metadata)
	}
}
