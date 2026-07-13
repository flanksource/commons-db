package sqlinspect

import "testing"

func TestBuildCatalogOrdersAndBoundsMetadata(t *testing.T) {
	rows := []columnRow{
		{schema: "z", relation: "events", relationType: "BASE TABLE", column: "id", dataType: "uuid", ordinal: 1},
		{schema: "public", relation: "users", relationType: "BASE TABLE", column: "name", dataType: "text", ordinal: 2},
		{schema: "public", relation: "users", relationType: "BASE TABLE", column: "id", dataType: "uuid", ordinal: 1},
		{schema: "public", relation: "active_users", relationType: "VIEW", column: "id", dataType: "uuid", ordinal: 1},
	}
	catalog := buildCatalog("postgres", "app", "public", []string{"postgres", "app"}, []string{"public", "z"}, rows, Limits{})
	if catalog.Database != "app" || catalog.DefaultSchema != "public" || len(catalog.Schemas) != 2 {
		t.Fatalf("catalog = %#v", catalog)
	}
	if len(catalog.Databases) != 2 || catalog.Databases[0] != "app" {
		t.Fatalf("databases = %#v", catalog.Databases)
	}
	if catalog.Schemas[0].Name != "public" || catalog.Schemas[0].Relations[0].Name != "active_users" {
		t.Fatalf("catalog is not deterministic: %#v", catalog.Schemas)
	}
	if catalog.Schemas[0].Relations[0].Type != "view" || len(catalog.Schemas[0].Relations[1].Columns) != 2 {
		t.Fatalf("relation metadata = %#v", catalog.Schemas[0].Relations)
	}

	truncated := buildCatalog("postgres", "app", "public", []string{"app"}, []string{"public", "z"}, rows, Limits{MaxRelations: 1, MaxColumns: 1})
	if !truncated.Truncated || truncated.TruncateReason == "" {
		t.Fatalf("expected bounded catalog: %#v", truncated)
	}
}

func TestBuildCatalogPreservesEmptySchemas(t *testing.T) {
	catalog := buildCatalog("postgres", "postgres", "public", []string{"postgres"}, []string{"public"}, nil, Limits{})
	if len(catalog.Schemas) != 1 || catalog.Schemas[0].Name != "public" || len(catalog.Schemas[0].Relations) != 0 {
		t.Fatalf("empty schema catalog = %#v", catalog)
	}
}

func TestInspectionQueriesSupportsBrowserDrivers(t *testing.T) {
	for _, driver := range []string{"postgres", "mysql", "sql_server", "clickhouse"} {
		if _, _, err := inspectionQueries(driver); err != nil {
			t.Errorf("%s: %v", driver, err)
		}
		if schemaInspectionQuery(driver) == "" {
			t.Errorf("%s: missing schema inspection query", driver)
		}
		if databaseInspectionQuery(driver) == "" {
			t.Errorf("%s: missing database inspection query", driver)
		}
	}
	if _, _, err := inspectionQueries("__unsupported__"); err == nil {
		t.Fatal("unsupported driver must fail")
	}
}
