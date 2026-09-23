package migrate

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing/fstest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	_ "modernc.org/sqlite"

	"github.com/flanksource/commons-db/internal/dbtarget"
)

const portableSchema = `
schema "public" {}

table "projects" {
  schema = schema.public
  column "id" {
    type = uuid
    null = false
  }
  column "external_key" {
    type = text
    null = false
  }
  column "payload" {
    type = jsonb
    null = false
    default = sql("'{}'")
  }
  column "created_at" {
    type = timestamptz
    null = false
  }
  primary_key { columns = [column.id] }
  unique "projects_external_key_key" { columns = [column.external_key] }
}

table "events" {
  schema = schema.public
  column "id" {
    type = uuid
    null = false
  }
  column "project_id" {
    type = uuid
    null = false
  }
  column "event_key" {
    type = text
    null = false
  }
  column "active" {
    type = bool
    null = false
    default = false
  }
  column "metadata" {
    type = jsonb
    null = false
    default = sql("'{}'")
  }
  primary_key { columns = [column.id] }
  foreign_key "events_project_id_fkey" {
    columns = [column.project_id]
    ref_columns = [table.projects.column.id]
    on_update = NO_ACTION
    on_delete = CASCADE
  }
  index "events_active_key" {
    unique = true
    columns = [column.project_id, column.event_key]
    where = "active"
  }
}
`

var _ = Describe("SQLite HCL target", func() {
	It("rebuilds a populated table for a new check only when requested", func(ctx context.Context) {
		dsn := filepath.Join(GinkgoT().TempDir(), "reshape.db")
		files := fstest.MapFS{"migrations/schema.hcl": &fstest.MapFile{Data: []byte(portableSchema)}}
		Expect(Apply(ctx, dsn, files, WithDir("migrations"))).To(Succeed())

		target, err := dbtarget.Parse(dsn)
		Expect(err).ToNot(HaveOccurred())
		database, err := sql.Open("sqlite", target.DSN)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(database.Close)
		_, err = database.ExecContext(ctx, `INSERT INTO projects (id, external_key, payload, created_at) VALUES ('p1', 'existing', '{}', CURRENT_TIMESTAMP)`)
		Expect(err).ToNot(HaveOccurred())
		_, err = database.ExecContext(ctx, `INSERT INTO events (id, project_id, event_key, active) VALUES ('e1', 'p1', 'build', true)`)
		Expect(err).ToNot(HaveOccurred())

		files["migrations/schema.hcl"] = &fstest.MapFile{Data: []byte(strings.Replace(portableSchema,
			`  unique "projects_external_key_key" { columns = [column.external_key] }`,
			`  unique "projects_external_key_key" { columns = [column.external_key] }
  check "projects_external_key_nonempty" { expr = "length(external_key) > 0" }`, 1))}
		Expect(Apply(ctx, dsn, files, WithDir("migrations"))).To(MatchError(ContainSubstring("only added tables, columns and indexes")))
		Expect(Apply(ctx, dsn, files, WithDir("migrations"), WithRebuilds())).To(Succeed())

		var key string
		Expect(database.QueryRowContext(ctx, `SELECT external_key FROM projects WHERE id = 'p1'`).Scan(&key)).To(Succeed())
		Expect(key).To(Equal("existing"))
		Expect(database.QueryRowContext(ctx, `SELECT project_id FROM events WHERE id = 'e1'`).Scan(&key)).To(Succeed())
		Expect(key).To(Equal("p1"))
		_, err = database.ExecContext(ctx, `INSERT INTO projects (id, external_key, payload, created_at) VALUES ('p2', '', '{}', CURRENT_TIMESTAMP)`)
		Expect(err).To(MatchError(ContainSubstring("CHECK constraint failed")))
		Expect(Apply(ctx, dsn, files, WithDir("migrations"), WithRebuilds())).To(Succeed())
	})

	It("rolls back a rebuild when existing rows violate the new constraint", func(ctx context.Context) {
		dsn := filepath.Join(GinkgoT().TempDir(), "rollback.db")
		files := fstest.MapFS{"schema.hcl": &fstest.MapFile{Data: []byte(portableSchema)}}
		Expect(Apply(ctx, dsn, files)).To(Succeed())
		target, err := dbtarget.Parse(dsn)
		Expect(err).ToNot(HaveOccurred())
		database, err := sql.Open("sqlite", target.DSN)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(database.Close)
		_, err = database.ExecContext(ctx, `INSERT INTO projects (id, external_key, payload, created_at) VALUES ('p1', '', '{}', CURRENT_TIMESTAMP)`)
		Expect(err).ToNot(HaveOccurred())
		files["schema.hcl"] = &fstest.MapFile{Data: []byte(strings.Replace(portableSchema,
			`  unique "projects_external_key_key" { columns = [column.external_key] }`,
			`  unique "projects_external_key_key" { columns = [column.external_key] }
  check "projects_external_key_nonempty" { expr = "length(external_key) > 0" }`, 1))}
		Expect(Apply(ctx, dsn, files, WithRebuilds())).To(MatchError(ContainSubstring("CHECK constraint failed")))
		var count int
		Expect(database.QueryRowContext(ctx, `SELECT count(*) FROM projects WHERE id = 'p1'`).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))
		Expect(database.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name = 'new_projects'`).Scan(&count)).To(Succeed())
		Expect(count).To(BeZero())
	})

	It("rejects dropping a column even with rebuilds enabled", func(ctx context.Context) {
		dsn := filepath.Join(GinkgoT().TempDir(), "drop.db")
		files := fstest.MapFS{"schema.hcl": &fstest.MapFile{Data: []byte(portableSchema)}}
		Expect(Apply(ctx, dsn, files)).To(Succeed())
		files["schema.hcl"] = &fstest.MapFile{Data: []byte(strings.Replace(portableSchema,
			`  column "external_key" {
    type = text
    null = false
  }
`, "", 1))}
		files["schema.hcl"].Data = []byte(strings.Replace(string(files["schema.hcl"].Data),
			`  unique "projects_external_key_key" { columns = [column.external_key] }`, "", 1))
		Expect(Apply(ctx, dsn, files, WithRebuilds())).To(MatchError(ContainSubstring(`drop column "external_key"`)))
	})

	It("refuses a rebuild that would discard an unmanaged trigger", func(ctx context.Context) {
		dsn := filepath.Join(GinkgoT().TempDir(), "trigger.db")
		files := fstest.MapFS{"schema.hcl": &fstest.MapFile{Data: []byte(portableSchema)}}
		Expect(Apply(ctx, dsn, files)).To(Succeed())
		target, err := dbtarget.Parse(dsn)
		Expect(err).ToNot(HaveOccurred())
		database, err := sql.Open("sqlite", target.DSN)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(database.Close)
		_, err = database.ExecContext(ctx, `CREATE TRIGGER project_audit AFTER INSERT ON projects BEGIN SELECT 1; END`)
		Expect(err).ToNot(HaveOccurred())
		files["schema.hcl"] = &fstest.MapFile{Data: []byte(strings.Replace(portableSchema,
			`  unique "projects_external_key_key" { columns = [column.external_key] }`,
			`  unique "projects_external_key_key" { columns = [column.external_key] }
  check "projects_external_key_nonempty" { expr = "length(external_key) > 0" }`, 1))}
		Expect(Apply(ctx, dsn, files, WithRebuilds())).To(MatchError(ContainSubstring("unmanaged trigger")))
		var count int
		Expect(database.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'trigger' AND name = 'project_audit'`).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))
	})

	It("applies one PostgreSQL-shaped HCL source as an idempotent SQLite schema", func(ctx context.Context) {
		dsn := filepath.Join(GinkgoT().TempDir(), "portable.db")
		files := fstest.MapFS{"migrations/schema.hcl": &fstest.MapFile{Data: []byte(portableSchema)}}
		Expect(Apply(ctx, dsn, files, WithDir("migrations"), WithName("portable"))).To(Succeed())

		target, err := dbtarget.Parse(dsn)
		Expect(err).ToNot(HaveOccurred())
		database, err := sql.Open("sqlite", target.DSN)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(database.Close)

		_, err = database.ExecContext(ctx, `INSERT INTO projects (id, external_key, payload, created_at) VALUES ('p1', 'project', '{}', CURRENT_TIMESTAMP)`)
		Expect(err).ToNot(HaveOccurred())

		files["migrations/schema.hcl"] = &fstest.MapFile{Data: []byte(strings.Replace(portableSchema,
			`  column "created_at" {`,
			"  column \"description\" {\n    type = text\n    null = true\n  }\n  column \"created_at\" {",
			1,
		))}
		Expect(Apply(ctx, dsn, files, WithDir("migrations"), WithName("portable"))).To(Succeed())
		Expect(Apply(ctx, dsn, files, WithDir("migrations"), WithName("portable"))).To(Succeed())

		var externalKey string
		var description sql.NullString
		Expect(database.QueryRowContext(ctx, `SELECT external_key, description FROM projects WHERE id = 'p1'`).
			Scan(&externalKey, &description)).To(Succeed())
		Expect(map[string]any{"external_key": externalKey, "description_valid": description.Valid}).To(Equal(map[string]any{
			"external_key": "project", "description_valid": false,
		}))

		_, err = database.ExecContext(ctx, `INSERT INTO events (id, project_id, event_key, active) VALUES ('e1', 'p1', 'build', true)`)
		Expect(err).ToNot(HaveOccurred())
		_, err = database.ExecContext(ctx, `INSERT INTO events (id, project_id, event_key, active) VALUES ('e2', 'p1', 'build', true)`)
		Expect(err).To(MatchError(ContainSubstring("UNIQUE constraint failed")))
		_, err = database.ExecContext(ctx, `INSERT INTO events (id, project_id, event_key, active) VALUES ('e3', 'missing', 'build', false)`)
		Expect(err).To(MatchError(ContainSubstring("FOREIGN KEY constraint failed")))
		_, err = database.ExecContext(ctx, `INSERT INTO projects (id, external_key, payload, created_at) VALUES ('p2', 'invalid', '{', CURRENT_TIMESTAMP)`)
		Expect(err).To(MatchError(ContainSubstring("CHECK constraint failed")))

		var idType, payloadType, createdAtType string
		Expect(database.QueryRowContext(ctx, `SELECT type FROM pragma_table_info('projects') WHERE name = 'id'`).Scan(&idType)).To(Succeed())
		Expect(database.QueryRowContext(ctx, `SELECT type FROM pragma_table_info('projects') WHERE name = 'payload'`).Scan(&payloadType)).To(Succeed())
		Expect(database.QueryRowContext(ctx, `SELECT type FROM pragma_table_info('projects') WHERE name = 'created_at'`).Scan(&createdAtType)).To(Succeed())
		Expect(map[string]string{"id": idType, "payload": payloadType, "created_at": createdAtType}).To(Equal(map[string]string{
			"id": "TEXT", "payload": "TEXT", "created_at": "datetime",
		}))
	})

	It("rejects PostgreSQL-only bundle features before opening SQLite", func(ctx context.Context) {
		dsn := filepath.Join(GinkgoT().TempDir(), "unsupported.db")
		files := fstest.MapFS{
			"migrations/schema.hcl": &fstest.MapFile{Data: []byte(portableSchema)},
			"migrations/view.sql":   &fstest.MapFile{Data: []byte("CREATE VIEW project_ids AS SELECT id FROM projects")},
		}
		Expect(Apply(ctx, dsn, files, WithDir("migrations"))).To(MatchError(ContainSubstring("do not support SQL migration files")))
	})

	It("rejects a canonical bundle with more than the public schema", func(ctx context.Context) {
		dsn := filepath.Join(GinkgoT().TempDir(), "schemas.db")
		files := fstest.MapFS{"schema.hcl": &fstest.MapFile{Data: []byte(portableSchema + `schema "audit" {}`)}}
		Expect(Apply(ctx, dsn, files)).To(MatchError(ContainSubstring("expected one schema, got 2")))
	})

	It("rejects a canonical check that collides with a projected JSON check", func(ctx context.Context) {
		dsn := filepath.Join(GinkgoT().TempDir(), "check-collision.db")
		files := fstest.MapFS{"schema.hcl": &fstest.MapFile{Data: []byte(`
schema "public" {}
table "documents" {
  schema = schema.public
  column "payload" {
    type = jsonb
    null = false
  }
  check "documents_payload_json" {
    expr = "payload <> ''"
  }
}
`)}}
		Expect(Apply(ctx, dsn, files)).To(MatchError(ContainSubstring("conflicts with generated SQLite JSON check")))
	})
})
