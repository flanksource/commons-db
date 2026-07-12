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
	profile.Columns = []query.ColumnDef{{Name: "id", Label: "ID", Type: query.ColumnTypeString}}
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
	if len(op.Parameters) != 1 || !op.Parameters[0].Required || op.Parameters[0].Clicky.Role != "filter" {
		t.Fatalf("profile filter parameter missing: %+v", op.Parameters)
	}
	if got := op.Parameters[0].Schema.Enum; len(got) != 2 || got[0] != "US" {
		t.Fatalf("profile enum missing: %+v", got)
	}
	item := op.Responses["200"].Content["application/json"].Schema.Items
	if item.Properties["id"].Extensions["x-clicky-label"] != "ID" {
		t.Fatalf("response column metadata missing: %+v", item.Properties["id"])
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
