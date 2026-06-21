package main

import (
	"testing"

	"github.com/flanksource/commons-db/query"
)

func sampleProfile(name string) query.Profile {
	return query.Profile{
		Name:     name,
		Provider: query.ProviderConfig{Type: "sql", Connection: "connection://db"},
		Query:    "select * from a where region = '{{.params.region}}'",
		Params:   []query.ParamDef{{Name: "region", Type: query.ParamTypeEnum, Options: []string{"US", "EU"}, Required: true}},
		Columns:  []query.ColumnDef{{Name: "id", Type: query.ColumnTypeString}},
	}
}

func TestProfileStoreRoundTrip(t *testing.T) {
	store, err := NewProfileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}

	want := sampleProfile("Sales Report")
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Get("Sales Report")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != want.Name || got.Query != want.Query {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
	}
	if len(got.Params) != 1 || got.Params[0].Name != "region" || !got.Params[0].Required {
		t.Fatalf("params not preserved: %+v", got.Params)
	}
	if len(got.Columns) != 1 || got.Columns[0].Name != "id" {
		t.Fatalf("columns not preserved: %+v", got.Columns)
	}
}

func TestProfileStoreListAndDelete(t *testing.T) {
	store, err := NewProfileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	for _, n := range []string{"Beta", "Alpha"} {
		if err := store.Save(sampleProfile(n)); err != nil {
			t.Fatalf("Save %q: %v", n, err)
		}
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].Name != "Alpha" || list[1].Name != "Beta" {
		t.Fatalf("expected [Alpha Beta] sorted, got %v", names(list))
	}

	if err := store.Delete("Alpha"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, _ = store.List()
	if len(list) != 1 || list[0].Name != "Beta" {
		t.Fatalf("expected [Beta] after delete, got %v", names(list))
	}
}

func TestProfileStoreSaveRequiresName(t *testing.T) {
	store, _ := NewProfileStore(t.TempDir())
	if err := store.Save(query.Profile{}); err == nil {
		t.Fatal("expected error saving a profile with no name")
	}
}

func TestProfileStoreGetMissing(t *testing.T) {
	store, _ := NewProfileStore(t.TempDir())
	if _, err := store.Get("nope"); err == nil {
		t.Fatal("expected error getting a missing profile")
	}
}

func names(ps []query.Profile) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}
