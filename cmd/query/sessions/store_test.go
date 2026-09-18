package sessions

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/flanksource/commons-db/query"
)

func sessionStoreDB() *gorm.DB {
	gdb := dbtest.ForGinkgo(dbtest.Options{Name: "query_sessions", LogName: "query-sessions-test"}).Gorm()
	Expect(gdb.AutoMigrate(&sessionRecord{}, &sessionEventRecord{})).To(Succeed())
	return gdb
}

func newGormStore(gdb *gorm.DB, retention time.Duration) *Store {
	store, err := NewStore(gdb, retention)
	Expect(err).ToNot(HaveOccurred())
	ginkgo.DeferCleanup(store.Close)
	return store
}

var _ = ginkgo.Describe("Store (gorm)", func() {
	ginkgo.Describe("as a session store", func() {
		describeSessionStoreContract(func() query.SessionStore {
			return newGormStore(sessionStoreDB(), 7*24*time.Hour)
		})
	})

	var (
		ctx   context.Context
		store *Store
		epoch time.Time
	)

	ginkgo.BeforeEach(func() {
		ctx, epoch = context.Background(), contractEpoch()
		store = newGormStore(sessionStoreDB(), time.Hour)
	})

	appendEvents := func(id string, count int) {
		for i := 1; i <= count; i++ {
			Expect(store.Append(ctx, query.Event{SessionID: id, Sequence: int64(i), Time: epoch, Row: query.Row{"n": float64(i)}})).To(Succeed())
		}
	}

	ginkgo.It("flushes a session's buffered events before writing its terminal status", func() {
		rec := contractFixture(epoch)[1]
		Expect(store.Begin(ctx, rec)).To(Succeed())
		appendEvents(rec.ID, 3)

		rec.State = query.SessionCompleted
		Expect(store.Update(ctx, rec.ID, rec.SessionStatus)).To(Succeed())
		events, err := store.Events(ctx, rec.ID)
		Expect(err).ToNot(HaveOccurred())
		Expect(events).To(HaveLen(3))
		Expect(events[1].Row).To(Equal(query.Row{"n": 2.0}))
	})

	ginkgo.It("flushes a full batch as it is appended", func() {
		rec := contractFixture(epoch)[1]
		Expect(store.Begin(ctx, rec)).To(Succeed())
		appendEvents(rec.ID, sessionEventBatchSize)
		events, err := store.Events(ctx, rec.ID)
		Expect(err).ToNot(HaveOccurred())
		Expect(events).To(HaveLen(sessionEventBatchSize))
	})

	ginkgo.It("prunes sessions stopped before the retention window, with their events", func() {
		old := contractFixture(epoch)[0]
		longAgo := time.Now().Add(-2 * time.Hour)
		old.StoppedAt = &longAgo
		fresh := contractFixture(epoch)[1]
		Expect(store.Begin(ctx, old)).To(Succeed())
		Expect(store.Begin(ctx, fresh)).To(Succeed())
		appendEvents(old.ID, 1)
		Expect(store.Flush()).To(Succeed())

		Expect(store.Prune(ctx)).To(Succeed())
		_, found, err := store.Get(ctx, old.ID)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
		events, err := store.Events(ctx, old.ID)
		Expect(err).ToNot(HaveOccurred())
		Expect(events).To(BeEmpty())
		_, found, err = store.Get(ctx, fresh.ID)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
	})

	ginkgo.It("keeps a batch whose write failed, so the retried terminal status lands with its events", func() {
		gdb := sessionStoreDB()
		store := newGormStore(gdb, time.Hour)
		var refusals atomic.Int32
		refusals.Store(1)
		const callback = "sessions-spec:refuse-events"
		Expect(gdb.Callback().Create().Before("gorm:create").Register(callback, func(db *gorm.DB) {
			if db.Statement.Table == "session_events" && refusals.Add(-1) >= 0 {
				_ = db.AddError(errors.New("disk full"))
			}
		})).To(Succeed())
		ginkgo.DeferCleanup(func() { Expect(gdb.Callback().Create().Remove(callback)).To(Succeed()) })

		rec := contractFixture(epoch)[1]
		Expect(store.Begin(ctx, rec)).To(Succeed())
		for i := int64(1); i <= 3; i++ {
			Expect(store.Append(ctx, query.Event{SessionID: rec.ID, Sequence: i, Time: epoch, Row: query.Row{"n": float64(i)}})).To(Succeed())
		}
		rec.State = query.SessionCompleted
		Expect(store.Update(ctx, rec.ID, rec.SessionStatus)).To(MatchError(ContainSubstring("disk full")))
		held, err := store.HasEvents(ctx, rec.ID)
		Expect(err).ToNot(HaveOccurred())
		Expect(held).To(BeTrue(), "the refused batch is still buffered")

		Expect(store.Update(ctx, rec.ID, rec.SessionStatus)).To(Succeed())
		events, err := store.Events(ctx, rec.ID)
		Expect(err).ToNot(HaveOccurred())
		Expect(events).To(HaveLen(3))
		stored, _, err := store.Get(ctx, rec.ID)
		Expect(err).ToNot(HaveOccurred())
		Expect(stored.State).To(Equal(query.SessionCompleted))
		held, err = store.HasEvents(ctx, "never-emitted")
		Expect(err).ToNot(HaveOccurred())
		Expect(held).To(BeFalse())
	})

	ginkgo.It("lists nothing when Allow refuses every profile", func() {
		for _, rec := range contractFixture(epoch) {
			Expect(store.Begin(ctx, rec)).To(Succeed())
		}
		page, err := store.List(ctx, query.SessionFilter{Allow: func(string) bool { return false }})
		Expect(err).ToNot(HaveOccurred())
		Expect(page).To(Equal(query.SessionPage{Items: []query.SessionRecord{}, Total: 0, Shared: true}))
	})
})
