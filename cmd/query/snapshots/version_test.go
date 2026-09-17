package snapshots_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/snapshots"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// A snapshot written by a build that stored its columns positionally is copied
// into the v2 file and named there, so that build can still read its own file.
var _ = Describe("opening a snapshot an older build wrote", func() {
	const id = "44444444-4444-4444-4444-444444444444"

	var (
		now    time.Time
		root   string
		legacy string
	)

	BeforeEach(func() {
		now = time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
		root = GinkgoT().TempDir()
		legacy = filepath.Join(root, id, "snapshot.sqlite")
		document, err := json.Marshal(map[string]any{
			"id": id, "created_at": now, "expires_at": now.Add(30 * time.Minute), "age": 30 * time.Minute,
			"connection_id": "55555555-5555-4555-8555-555555555555", "connection_name": "reconciliation-abc123def456",
			"connection_namespace": "reconciliations", "base_profile": "reconciliations/abc123def456/results",
			"stats": map[string]int{"matched": 1, "only_source": 1}, "source": "outgoing", "dest": "incoming",
			"profiles": []map[string]any{{
				"name": "reconciliations/abc123def456/results", "table": "reconcile_rows", "rows": 2,
				"columns": []map[string]any{
					{"name": "key"}, {"name": "Outcome Status"}, {"name": "payload", "type": "json"},
					{"name": "row_id", "type": "number", "hidden": true},
				},
			}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(filepath.Dir(legacy), 0o700)).To(Succeed())
		database, err := sql.Open("sqlite", legacy)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`CREATE TABLE "reconcile_rows" ("c0" TEXT, "c1" TEXT, "c2" TEXT, "c3" NUMERIC);
			CREATE UNIQUE INDEX "reconcile_rows_row_id" ON "reconcile_rows" ("c3");
			INSERT INTO "reconcile_rows" VALUES ('A', 'matched', '{"ok":true}', 1), ('B', 'only_source', NULL, 2);
			CREATE TABLE "_metadata" (version INTEGER NOT NULL, document TEXT NOT NULL)`)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`INSERT INTO "_metadata" (version, document) VALUES (1, ?)`, string(document))
		Expect(err).NotTo(HaveOccurred())
		Expect(database.Close()).To(Succeed())
	})

	open := func() *snapshots.Manager {
		manager, err := snapshots.New(snapshots.Options{Dir: root, MaxAge: time.Hour, Now: func() time.Time { return now }})
		Expect(err).NotTo(HaveOccurred())
		Expect(manager.Prepare()).To(Succeed())
		return manager
	}

	rowsOf := func(manager *snapshots.Manager) []query.Row {
		descriptor, err := manager.Describe(context.Background(), id)
		Expect(err).NotTo(HaveOccurred())
		profile, err := manager.Get(context.Background(), descriptor.Profile)
		Expect(err).NotTo(HaveOccurred())
		ctx := dbcontext.New().WithConnectionResolver(manager.ResolveConnection).WithConnectionLeaseResolver(manager.AcquireConnection)
		result, err := query.Execute(ctx, profile, nil)
		Expect(err).NotTo(HaveOccurred())
		rows := make([]query.Row, len(result.Rows))
		for index, row := range result.Rows {
			rows[index] = query.Row{"key": row["key"], "Outcome Status": row["Outcome Status"], "payload": row["payload"]}
		}
		return rows
	}

	physicalColumns := func(path string) []string {
		database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(database.Close()).To(Succeed()) }()
		rows, err := database.Query(`SELECT name FROM pragma_table_info('reconcile_rows') ORDER BY cid`)
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(rows.Close()).To(Succeed()) }()
		var names []string
		for rows.Next() {
			var name string
			Expect(rows.Scan(&name)).To(Succeed())
			names = append(names, name)
		}
		return names
	}

	expectedRows := []query.Row{
		{"key": "A", "Outcome Status": "matched", "payload": map[string]any{"ok": true}},
		{"key": "B", "Outcome Status": "only_source", "payload": nil},
	}

	It("copies it into v2 with named columns and serves the same rows, leaving the v1 file untouched", func() {
		original, err := os.ReadFile(legacy)
		Expect(err).NotTo(HaveOccurred())

		manager := open()
		rows := rowsOf(manager)
		Expect(manager.Close()).To(Succeed())

		Expect(map[string]any{"rows": rows, "physical": physicalColumns(filepath.Join(root, id, "v2", "snapshot.sqlite"))}).To(Equal(map[string]any{
			"rows": expectedRows, "physical": []string{"key", "Outcome_Status", "payload", "row_id"},
		}))
		Expect(os.ReadFile(legacy)).To(Equal(original), "the v1 file changed")
	})

	It("reuses the v2 copy on a later start without reading the v1 file again", func() {
		first := open()
		Expect(first.Close()).To(Succeed())
		Expect(os.Remove(legacy)).To(Succeed())

		second := open()
		defer func() { Expect(second.Close()).To(Succeed()) }()
		Expect(rowsOf(second)).To(Equal(expectedRows))
	})
})
