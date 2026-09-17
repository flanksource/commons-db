package app

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/sessions"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/flanksource/commons-db/query"
)

var _ = Describe("sessions table migration", func() {
	It("rebuilds legacy rows as session records and serves the new columns to the store", func() {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "query_sessions_migration", LogName: "query-sessions-migration-test"})
		Expect(handle.Gorm().Exec(`CREATE TABLE sessions (
			id text PRIMARY KEY, profile_name text NOT NULL, kind text NOT NULL, params jsonb, state text NOT NULL,
			error text, event_count bigint NOT NULL DEFAULT 0, started_at timestamptz NOT NULL, stopped_at timestamptz,
			created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now())`).Error).To(Succeed())
		Expect(handle.Gorm().Exec(`INSERT INTO sessions (id, profile_name, kind, params, state, error, event_count, started_at, stopped_at, updated_at)
			VALUES ('legacy', 'cpu-top', 'top', '{"namespace":"prod"}', 'failed', 'boom', 7,
			'2026-09-01T10:00:00Z', '2026-09-01T10:05:00Z', '2026-09-01T10:05:00Z')`).Error).To(Succeed())

		ctx := GinkgoT().Context()
		Expect(migrateSchema(ctx, handle.DSN())).To(Succeed())
		Expect(migrateSchema(ctx, handle.DSN())).To(Succeed(), "migration must be idempotent")

		store, err := sessions.NewStore(handle.Gorm(), 365*24*time.Hour)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(store.Close)

		legacy, found, err := store.Get(ctx, "legacy")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		stopped := time.Date(2026, 9, 1, 10, 5, 0, 0, time.UTC)
		Expect(legacy).To(And(
			HaveField("ID", "legacy"), HaveField("Profile", "cpu-top"), HaveField("Kind", query.KindTop),
			HaveField("Role", query.SessionRoleCapture), HaveField("Params", map[string]any{"namespace": "prod"}),
			HaveField("State", query.SessionFailed), HaveField("Error", "boom"), HaveField("EventCount", int64(7)),
		))
		Expect(legacy.StartedAt.Equal(time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))).To(BeTrue())
		Expect(legacy.StoppedAt).ToNot(BeNil())
		Expect(legacy.StoppedAt.Equal(stopped)).To(BeTrue())

		now := time.Now().UTC().Truncate(time.Millisecond)
		fresh := query.SessionRecord{
			SessionStart: query.SessionStart{ID: "fresh", Profile: "trace-capture/jvm_trace", Kind: query.KindCapture,
				Role: query.SessionRoleCapture, Principal: "admin", RestartOf: "legacy", StartedAt: now},
			SessionStatus: query.SessionStatus{State: query.SessionRunning, UpdatedAt: now, HeartbeatAt: now},
		}
		Expect(store.Begin(ctx, fresh)).To(Succeed())
		page, err := store.List(ctx, query.SessionFilter{Principal: []string{"adm*"}, Sort: "principal"})
		Expect(err).ToNot(HaveOccurred())
		Expect(page.Total).To(Equal(1))
		lineage, err := store.Lineage(ctx, []string{"legacy"})
		Expect(err).ToNot(HaveOccurred())
		Expect(lineage).To(Equal(map[string][]string{"legacy": {"fresh"}}))
	})
})
