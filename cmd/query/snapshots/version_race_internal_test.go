package snapshots

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Two processes reloading the same snapshot directory both find the v2 file
// missing and both copy; the one that links second must use the copy already
// there rather than fail to reload a sound snapshot.
var _ = Describe("copying a version 1 snapshot two processes race over", func() {
	const id = "66666666-6666-4666-8666-666666666666"

	var dir, target string

	BeforeEach(func() {
		now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
		dir = filepath.Join(GinkgoT().TempDir(), id)
		target = snapshotFile(dir)
		document, err := json.Marshal(map[string]any{
			"id": id, "created_at": now, "expires_at": now.Add(30 * time.Minute), "age": 30 * time.Minute,
			"connection_id": "55555555-5555-4555-8555-555555555555", "connection_name": "reconciliation-abc123def456",
			"connection_namespace": "reconciliations", "base_profile": "reconciliations/abc123def456/results",
			"source": "outgoing", "dest": "incoming",
			"profiles": []map[string]any{{
				"name": "reconciliations/abc123def456/results", "table": "reconcile_rows", "rows": 1,
				"columns": []map[string]any{{"name": "key"}, {"name": "row_id", "type": "number", "hidden": true}},
			}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		database, err := sql.Open("sqlite", filepath.Join(dir, snapshotFileName))
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`CREATE TABLE "reconcile_rows" ("c0" TEXT, "c1" NUMERIC);
			INSERT INTO "reconcile_rows" VALUES ('A', 1);
			CREATE TABLE "_metadata" (version INTEGER NOT NULL, document TEXT NOT NULL)`)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`INSERT INTO "_metadata" (version, document) VALUES (1, ?)`, string(document))
		Expect(err).NotTo(HaveOccurred())
		Expect(database.Close()).To(Succeed())
	})

	It("keeps the copy another process placed first", func() {
		placedFirst := []byte("the copy another process linked into place")
		Expect(os.MkdirAll(filepath.Dir(target), 0o700)).To(Succeed())
		Expect(os.WriteFile(target, placedFirst, 0o600)).To(Succeed())

		Expect(copyV1Snapshot(context.Background(), dir, target)).To(Succeed())

		Expect(os.ReadFile(target)).To(Equal(placedFirst), "the winner's copy was replaced")
	})

	It("leaves no half-migrated copy behind in the target directory", func() {
		Expect(copyV1Snapshot(context.Background(), dir, target)).To(Succeed())

		entries, err := os.ReadDir(filepath.Dir(target))
		Expect(err).NotTo(HaveOccurred())
		names := make([]string, len(entries))
		for index, entry := range entries {
			names[index] = entry.Name()
		}
		Expect(names).To(Equal([]string{snapshotFileName}))
	})
})
