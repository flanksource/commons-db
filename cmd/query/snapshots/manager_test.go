package snapshots_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/cmd/query/snapshots"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	_ "github.com/flanksource/commons-db/query/providers"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	_ "modernc.org/sqlite"
)

var _ = Describe("Manager", func() {
	var (
		now     time.Time
		manager *snapshots.Manager
		root    string
	)

	BeforeEach(func() {
		now = time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
		root = GinkgoT().TempDir()
		var err error
		manager, err = snapshots.New(snapshots.Options{
			Dir: root, MaxAge: time.Hour, Now: func() time.Time { return now },
		})
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		Expect(manager.Close()).To(Succeed())
	})

	It("materializes a reconciliation as a virtual connection and profile", func() {
		descriptor, err := manager.Create(context.Background(), reconciliation(), 30*time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(descriptor.RowCount).To(Equal(2))
		Expect(descriptor.Profile).To(HavePrefix("reconciliations/"))
		Expect(descriptor.Connection).To(HavePrefix("connection://reconciliations/"))

		profile, err := manager.Get(context.Background(), descriptor.Profile)
		Expect(err).NotTo(HaveOccurred())
		Expect(profile.Provider.Connection).To(Equal(descriptor.Connection))
		Expect(profile.Order).To(Equal(query.Order{{Column: "row_id", Unique: true}}))

		connection, err := manager.ResolveConnection(descriptor.Connection)
		Expect(err).NotTo(HaveOccurred())
		Expect(connection.Virtual).To(BeTrue())
		Expect(connection.ReadOnly).To(BeTrue())
		Expect(connection.URL).To(ContainSubstring("mode=ro"))

		info, err := os.Stat(filepath.Join(root, descriptor.ID, "snapshot.sqlite"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
	})

	It("does not let a run exceed the configured expiry age", func() {
		_, err := manager.Create(context.Background(), reconciliation(), 2*time.Hour)
		Expect(err).To(MatchError(ContainSubstring("cannot exceed")))
	})

	It("uses sliding expiry for access but not catalog listing", func() {
		descriptor, err := manager.Create(context.Background(), reconciliation(), 10*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		now = now.Add(9 * time.Minute)
		Expect(manager.ListConnections()).To(HaveLen(1))
		now = now.Add(2 * time.Minute)
		_, err = manager.Get(context.Background(), descriptor.Profile)
		Expect(err).To(MatchError(snapshots.ErrExpired))
		_, err = manager.ResolveConnection(descriptor.Connection)
		Expect(err).To(MatchError(snapshots.ErrExpired))
	})

	It("does not extend expiry while profile metadata is inspected", func() {
		descriptor, err := manager.Create(context.Background(), reconciliation(), 10*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		now = now.Add(9 * time.Minute)
		_, err = manager.Peek(context.Background(), descriptor.Profile)
		Expect(err).NotTo(HaveOccurred())
		now = now.Add(2 * time.Minute)
		_, err = manager.Get(context.Background(), descriptor.Profile)
		Expect(err).To(MatchError(snapshots.ErrExpired))
	})

	It("keeps an expired snapshot until its active query lease is released", func() {
		descriptor, err := manager.Create(context.Background(), reconciliation(), 10*time.Minute)
		Expect(err).NotTo(HaveOccurred())
		release, err := manager.AcquireConnection(descriptor.Connection)
		Expect(err).NotTo(HaveOccurred())

		now = now.Add(11 * time.Minute)
		Expect(manager.ListConnections()).To(HaveLen(1))
		release()
		now = now.Add(11 * time.Minute)
		Expect(manager.ListConnections()).To(BeEmpty())
		_, err = manager.ResolveConnection(descriptor.Connection)
		Expect(err).To(MatchError(snapshots.ErrExpired))
	})

	It("materializes CEL rows and an ordered export projection", func() {
		descriptor, err := manager.Create(context.Background(), reconciliation(), 0)
		Expect(err).NotTo(HaveOccurred())

		transformed, err := manager.Materialize(context.Background(), profiles.ReconcileMaterializeOptions{
			SnapshotID: descriptor.ID,
			Profile:    descriptor.Profile,
			CEL:        `dyn(rows).map(row, {"identity": row.key, "result": row.outcome})`,
			Columns:    []string{"result", "identity"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(transformed.RowCount).To(Equal(2))
		Expect(transformed.ColumnNames()).To(Equal([]string{"result", "identity"}))
	})

	It("keeps the declared schema when projecting an empty reconciliation", func() {
		empty := reconciliation()
		empty.Rows = nil
		empty.Stats = query.ReconcileStats{}
		descriptor, err := manager.Create(context.Background(), empty, 0)
		Expect(err).NotTo(HaveOccurred())

		projected, err := manager.Materialize(context.Background(), profiles.ReconcileMaterializeOptions{
			SnapshotID: descriptor.ID,
			Profile:    descriptor.Profile,
			Columns:    []string{"outcome", "key"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(projected.RowCount).To(BeZero())
		Expect(projected.ColumnNames()).To(Equal([]string{"outcome", "key"}))
	})

	It("reuses one materialization for concurrent identical exports", func() {
		descriptor, err := manager.Create(context.Background(), reconciliation(), 0)
		Expect(err).NotTo(HaveOccurred())
		const workers = 8
		start := make(chan struct{})
		results := make(chan profiles.ReconcileSnapshotDescriptor, workers)
		errors := make(chan error, workers)
		for range workers {
			go func() {
				<-start
				result, err := manager.Materialize(context.Background(), profiles.ReconcileMaterializeOptions{
					SnapshotID: descriptor.ID,
					Profile:    descriptor.Profile,
					Columns:    []string{"outcome", "key"},
				})
				results <- result
				errors <- err
			}()
		}
		close(start)

		profiles := map[string]bool{}
		for range workers {
			Expect(<-errors).NotTo(HaveOccurred())
			profiles[(<-results).Profile] = true
		}
		Expect(profiles).To(HaveLen(1))
	})

	It("executes the generated profile through the normal SQL provider", func() {
		descriptor, err := manager.Create(context.Background(), reconciliation(), 0)
		Expect(err).NotTo(HaveOccurred())
		profile, err := manager.Get(context.Background(), descriptor.Profile)
		Expect(err).NotTo(HaveOccurred())
		ctx := dbcontext.New().
			WithConnectionResolver(manager.ResolveConnection).
			WithConnectionLeaseResolver(manager.AcquireConnection)

		result, err := query.Execute(ctx, profile, map[string]any{"filter.outcome": "matched"})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Rows).To(HaveLen(1))
		Expect(result.Rows[0]["outgoing_payload"]).To(Equal(map[string]any{"ok": true}))
	})
})

func reconciliation() *query.ReconcileResult {
	source := query.Profile{Name: "outgoing", Columns: []query.ColumnDef{{Name: "id"}, {Name: "payload", Type: query.ColumnTypeJSON}}}
	dest := query.Profile{Name: "incoming", Columns: []query.ColumnDef{{Name: "id"}}}
	return &query.ReconcileResult{
		Source: source.Name, Dest: dest.Name, SourceProfile: source, DestProfile: dest,
		Stats: query.ReconcileStats{Matched: 1, OnlySource: 1},
		Rows: []query.ReconcileRow{
			{Key: "A", Status: query.ReconcileMatched, Source: query.Row{"id": "A", "payload": map[string]any{"ok": true}}, Dest: query.Row{"id": "A"}},
			{Key: "B", Status: query.ReconcileOnlySource, Source: query.Row{"id": "B"}},
		},
	}
}
