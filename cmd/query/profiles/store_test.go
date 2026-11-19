package profiles

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/commons-db/query"
	"sigs.k8s.io/yaml"
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
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}

	want := sampleProfile("Sales Report")
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Get(context.Background(), "Sales Report")
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
	bySlug, err := store.Get(context.Background(), "profile-sales-report")
	if err != nil || bySlug.Name != want.Name {
		t.Fatalf("profile slug lookup failed: %+v err=%v", bySlug, err)
	}
	info, err := os.Stat(filepath.Join(store.Dir, "sales-report.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("profile file mode = %o, want 600", gotMode)
	}
}

func TestFileStoreCreatesItsDirOnlyToSave(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profiles")
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("NewFileStore created %s (stat: %v)", dir, err)
	}
	list, err := store.List(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("List of a missing dir: %v, %v", names(list), err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("List created %s (stat: %v)", dir, err)
	}

	if err := store.Save(context.Background(), sampleProfile("Sales Report")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Save did not create %s: %v", dir, err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o700 {
		t.Fatalf("profiles dir mode = %o, want 700", gotMode)
	}
	if _, err := store.Get(context.Background(), "Sales Report"); err != nil {
		t.Fatalf("Get: %v", err)
	}
}

func TestProfileStoreListAndDelete(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	for _, n := range []string{"Beta", "Alpha"} {
		if err := store.Save(context.Background(), sampleProfile(n)); err != nil {
			t.Fatalf("Save %q: %v", n, err)
		}
	}

	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].Name != "Alpha" || list[1].Name != "Beta" {
		t.Fatalf("expected [Alpha Beta] sorted, got %v", names(list))
	}

	if err := store.Delete(context.Background(), "Alpha"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, _ = store.List(context.Background())
	if len(list) != 1 || list[0].Name != "Beta" {
		t.Fatalf("expected [Beta] after delete, got %v", names(list))
	}
}

func TestProfileStoreListRejectsMalformedQueryProfile(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	malformed := []byte(`profile: Broken
params:
  region:
    required: true
`)
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), malformed, 0o600); err != nil {
		t.Fatalf("write malformed query profile: %v", err)
	}

	if _, err := store.List(context.Background()); err == nil {
		t.Fatal("expected malformed current query profile to fail listing")
	}
}

func TestProfileStoreSaveRequiresName(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	if err := store.Save(context.Background(), query.Profile{}); err == nil {
		t.Fatal("expected error saving a profile with no name")
	}
}

func TestProfileStoreSaveRejectsInvalidColumnPresentation(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	profile := sampleProfile("Invalid presentation")
	profile.Columns[0].Unit = "percent"

	if err := store.Save(context.Background(), profile); err == nil {
		t.Fatal("expected error saving a string column with a numeric unit")
	}
}

func TestProfileStoreGetMissing(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	if _, err := store.Get(context.Background(), "nope"); err == nil {
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

func TestFileStoreReadsQueryFileFromItsDirectory(t *testing.T) {
	dir := t.TempDir()
	program := "DELETE FROM a\nGO\nSELECT count(*) AS n FROM a\n"
	if err := os.WriteFile(filepath.Join(dir, "purge.sql"), []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	authored := "profile: purge\nprovider: {type: sqlite}\nqueryFile: purge.sql\n"
	if err := os.WriteFile(filepath.Join(dir, "purge.yaml"), []byte(authored), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), "purge")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Query != program || got.Validate() != nil {
		t.Fatalf("Get read query %q from queryFile %q in %q (validate: %v), want the file's program", got.Query, got.QueryFile, got.QueryDir, got.Validate())
	}
	if err := store.Save(context.Background(), got); err != nil {
		t.Fatalf("Save: %v", err)
	}
	saved, err := os.ReadFile(filepath.Join(dir, "purge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var reread map[string]any
	if err := yaml.Unmarshal(saved, &reread); err != nil {
		t.Fatal(err)
	}
	if reread["queryFile"] != "purge.sql" || reread["query"] != nil {
		t.Fatalf("Save wrote %s, want the queryFile reference and no inlined query", saved)
	}
}
