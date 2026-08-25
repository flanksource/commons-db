package snapshots

import (
	"context"
	"database/sql"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/query"
)

var _ = Describe("snapshot metadata", func() {
	open := func() *sql.DB {
		database, err := sql.Open("sqlite", filepath.Join(GinkgoT().TempDir(), "snapshot.sqlite"))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })
		return database
	}

	record := snapshotMetadata{
		ID:                  "3f1c8a24-9f2b-4c31-8f0e-2a7b6d5c4e13",
		CreatedAt:           time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC),
		ExpiresAt:           time.Date(2026, 8, 12, 9, 30, 0, 0, time.UTC),
		Age:                 30 * time.Minute,
		ConnectionID:        "6c1f2b90-1c4d-4f8e-9a2b-7d5c4e130000",
		ConnectionName:      "reconciliation-abc123def456",
		ConnectionNamespace: "reconciliations",
		BaseProfile:         "reconciliations/abc123def456/results",
		Stats:               query.ReconcileStats{Matched: 8, OnlySource: 3, DupKeys: 1},
		Source:              "orders-emitted",
		Dest:                "orders-ingested",
		SourceTruncated:     true,
		Reconcile: &profiles.ReconcileSnapshotProvenance{
			Config: query.ReconcileConfig{
				Dest:          "orders-ingested",
				SourceFilters: map[string]string{"region": "eu"},
				DestFilters:   map[string]string{"tenant": "acme"},
				ReconcileSpec: query.ReconcileSpec{
					Range:      &query.KeyRange{From: "ord002", To: "ord004"},
					Key:        query.KeySpec{Columns: []string{"order_id"}},
					TimeColumn: "created_at",
				},
			},
		},
		Profiles: []snapshotProfileMetadata{{
			Name:  "reconciliations/abc123def456/results",
			Table: "reconcile_rows",
			Rows:  12,
			Columns: []query.ColumnDef{
				{Name: "key", Label: "Key"},
				{Name: "outcome", Type: query.ColumnTypeStatus},
				{Name: "payload", Type: query.ColumnTypeJSON, JSONPath: "$"},
				{Name: "row_id", Type: query.ColumnTypeNumber, Hidden: true},
			},
		}},
	}

	It("round-trips everything Prepare needs to rebuild a snapshot", func() {
		database := open()
		Expect(writeSnapshotMetadata(context.Background(), database, record)).To(Succeed())

		Expect(readSnapshotMetadata(context.Background(), database)).To(Equal(record))
	})

	// The whole record is rewritten on every materialization, so a second write
	// must replace rather than accumulate.
	It("replaces the stored record rather than appending to it", func() {
		database := open()
		Expect(writeSnapshotMetadata(context.Background(), database, record)).To(Succeed())
		second := record
		second.Profiles = append(append([]snapshotProfileMetadata{}, record.Profiles...), snapshotProfileMetadata{
			Name: "reconciliations/abc123def456/results/materialized-0011223344", Table: "materialized_00112233", Rows: 12,
		})
		Expect(writeSnapshotMetadata(context.Background(), database, second)).To(Succeed())

		var rows int
		Expect(database.QueryRow(`SELECT count(*) FROM "_metadata"`).Scan(&rows)).To(Succeed())
		Expect(rows).To(Equal(1))
		stored, err := readSnapshotMetadata(context.Background(), database)
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.Profiles).To(HaveLen(2))
	})

	It("refuses a document written by a newer build", func() {
		database := open()
		Expect(writeSnapshotMetadata(context.Background(), database, record)).To(Succeed())
		_, err := database.Exec(`UPDATE "_metadata" SET version = ?`, snapshotMetadataVersion+1)
		Expect(err).NotTo(HaveOccurred())

		_, err = readSnapshotMetadata(context.Background(), database)
		Expect(err).To(MatchError(ContainSubstring("newer than this build understands")))
	})

	It("reports a file that carries no metadata at all", func() {
		_, err := readSnapshotMetadata(context.Background(), open())
		Expect(err).To(HaveOccurred())
	})
})
