package snapshots_test

import (
	"context"
	"database/sql"
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
)

// A snapshot is addressed by a URL someone can bookmark or share, so what it
// survives is part of its contract: a restart must not void a link that has not
// expired, and must not revive one that has.
var _ = Describe("reloading snapshots after a restart", func() {
	var (
		now  time.Time
		root string
	)

	BeforeEach(func() {
		now = time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
		root = GinkgoT().TempDir()
	})

	// open builds a manager over the same directory, standing in for the next
	// process to start.
	open := func(maxAge time.Duration) *snapshots.Manager {
		manager, err := snapshots.New(snapshots.Options{
			Dir: root, MaxAge: maxAge, Now: func() time.Time { return now },
		})
		Expect(err).NotTo(HaveOccurred())
		return manager
	}

	It("serves the same snapshot, connection and rows after a restart", func() {
		first := open(time.Hour)
		Expect(first.Prepare()).To(Succeed())
		created, err := first.Create(context.Background(), reconciliation(), 30*time.Minute)
		Expect(err).NotTo(HaveOccurred())
		projected, err := first.Materialize(context.Background(), profiles.ReconcileMaterializeOptions{
			SnapshotID: created.ID, Profile: created.Profile, Columns: []string{"outcome", "key"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Close()).To(Succeed())

		second := open(time.Hour)
		Expect(second.Prepare()).To(Succeed())
		defer func() { Expect(second.Close()).To(Succeed()) }()

		reloaded, err := second.Describe(context.Background(), created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(reloaded.ID).To(Equal(created.ID))
		Expect(reloaded.Profile).To(Equal(created.Profile))
		Expect(reloaded.RowCount).To(Equal(created.RowCount))
		Expect(reloaded.Stats).To(Equal(created.Stats))
		Expect(reloaded.Columns).To(Equal(created.Columns))
		// A regenerated connection uuid would orphan every cached reference.
		Expect(reloaded.ConnectionID).To(Equal(created.ConnectionID))
		Expect(reloaded.Connection).To(Equal(created.Connection))

		// The provenance is the reason the results URL can drop the bench query.
		Expect(reloaded.Reconcile).NotTo(BeNil())
		Expect(reloaded.Reconcile.Config.Dest).To(Equal("incoming"))
		Expect(reloaded.Reconcile.Config.Key.CEL).To(Equal("row.id"))
		Expect(reloaded.Reconcile.Config.SourceFilters).To(Equal(map[string]string{"region": "eu"}))
		Expect(reloaded.Reconcile.Execution.Source.Diagnostics.Request.Rendered).To(
			Equal("select * from outgoing where region = 'eu'"))

		// Both the reconciliation and the projection materialized from it come back.
		Expect(second.Get(context.Background(), created.Profile)).Error().NotTo(HaveOccurred())
		Expect(second.Get(context.Background(), projected.Profile)).Error().NotTo(HaveOccurred())

		// The strongest check: the rows are still queryable through the ordinary
		// SQL path, so the reloaded profile is not just metadata-shaped.
		profile, err := second.Get(context.Background(), created.Profile)
		Expect(err).NotTo(HaveOccurred())
		ctx := dbcontext.New().
			WithConnectionResolver(second.ResolveConnection).
			WithConnectionLeaseResolver(second.AcquireConnection)
		result, err := query.Execute(ctx, profile, map[string]any{"filter.outcome": "matched"})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Rows).To(HaveLen(1))
		Expect(result.Rows[0]["outgoing_payload"]).To(Equal(map[string]any{"ok": true}))
	})

	It("drops a snapshot whose deadline passed while the process was down", func() {
		first := open(time.Hour)
		Expect(first.Prepare()).To(Succeed())
		created, err := first.Create(context.Background(), reconciliation(), 10*time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Close()).To(Succeed())

		now = now.Add(11 * time.Minute)
		second := open(time.Hour)
		Expect(second.Prepare()).To(Succeed())
		defer func() { Expect(second.Close()).To(Succeed()) }()

		// Expired, not missing: the reload tombstones what it drops, so a stale
		// bookmark is told the run aged out rather than that it never existed.
		_, err = second.Describe(context.Background(), created.ID)
		Expect(err).To(MatchError(snapshots.ErrExpired))
		Expect(second.ListConnections()).To(BeEmpty())
		Expect(filepath.Join(root, created.ID)).ToNot(BeADirectory())
	})

	It("keeps a snapshot whose deadline has not passed", func() {
		first := open(time.Hour)
		Expect(first.Prepare()).To(Succeed())
		created, err := first.Create(context.Background(), reconciliation(), 30*time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Close()).To(Succeed())

		now = now.Add(20 * time.Minute)
		second := open(time.Hour)
		Expect(second.Prepare()).To(Succeed())
		defer func() { Expect(second.Close()).To(Succeed()) }()

		Expect(second.Describe(context.Background(), created.ID)).Error().NotTo(HaveOccurred())
	})

	It("discards a directory it cannot read rather than failing to start", func() {
		Expect(os.MkdirAll(filepath.Join(root, "11111111-1111-1111-1111-111111111111"), 0o700)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(root, "11111111-1111-1111-1111-111111111111", "snapshot.sqlite"),
			[]byte("this is not a database"), 0o600)).To(Succeed())

		// A valid SQLite file written by a build that stored no metadata.
		legacy := filepath.Join(root, "22222222-2222-2222-2222-222222222222")
		Expect(os.MkdirAll(legacy, 0o700)).To(Succeed())
		database, err := sql.Open("sqlite", filepath.Join(legacy, "snapshot.sqlite"))
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`CREATE TABLE reconcile_rows ("c0" TEXT)`)
		Expect(err).NotTo(HaveOccurred())
		Expect(database.Close()).To(Succeed())

		manager := open(time.Hour)
		Expect(manager.Prepare()).To(Succeed())
		defer func() { Expect(manager.Close()).To(Succeed()) }()

		Expect(manager.ListConnections()).To(BeEmpty())
		Expect(filepath.Join(root, "11111111-1111-1111-1111-111111111111")).ToNot(BeADirectory())
		Expect(legacy).ToNot(BeADirectory())
	})

	// The operator lowered --reconcile-snapshot-max-age while a longer-lived
	// snapshot was on disk; it cannot outlive the new ceiling.
	It("clamps a reloaded snapshot to a lowered server maximum", func() {
		first := open(time.Hour)
		Expect(first.Prepare()).To(Succeed())
		created, err := first.Create(context.Background(), reconciliation(), time.Hour)
		Expect(err).NotTo(HaveOccurred())
		Expect(created.IdleAge).To(Equal(time.Hour))
		Expect(first.Close()).To(Succeed())

		second := open(time.Hour)
		// This is the server's startup order, and it only works because the
		// reload happens inside Prepare rather than before it.
		Expect(second.SetMaxAge(10 * time.Minute)).To(Succeed())
		Expect(second.Prepare()).To(Succeed())
		defer func() { Expect(second.Close()).To(Succeed()) }()

		reloaded, err := second.Describe(context.Background(), created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(reloaded.IdleAge).To(Equal(10 * time.Minute))
	})

	It("reports an id it has never seen as not found", func() {
		manager := open(time.Hour)
		Expect(manager.Prepare()).To(Succeed())
		defer func() { Expect(manager.Close()).To(Succeed()) }()

		_, err := manager.Describe(context.Background(), "33333333-3333-3333-3333-333333333333")
		Expect(err).To(MatchError(profiles.ErrSnapshotNotFound))
	})
})
