package connections

import (
	"context"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("connection health cache", func() {
	var (
		id        string
		updatedAt time.Time
		ctx       dbcontext.Context
	)

	BeforeEach(func() {
		id = uuid.NewString()
		updatedAt = time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
		ctx = dbcontext.NewContext(context.Background())
		DeferCleanup(func() { forgetConnectionHealth(id) })
	})

	storeHealthy := func() connectionHealthResult {
		result := connectionHealthResult{
			ID: id, State: connectionHealthHealthy, Detail: "PostgreSQL 17.2",
			CheckedAt: updatedAt.Add(time.Hour), ConnectionUpdatedAt: updatedAt,
		}
		storeConnectionHealth(ctx, result)
		return result
	}

	It("serves a stored result for an unchanged connection", func() {
		stored := storeHealthy()

		cached, ok := cachedConnectionHealth(id, updatedAt)

		Expect(ok).To(BeTrue())
		Expect(cached.State).To(Equal(stored.State))
		Expect(cached.Detail).To(Equal(stored.Detail))
		Expect(cached.Cached).To(BeTrue(), "a cache hit must be distinguishable from a fresh probe")
	})

	It("misses once the connection row has been edited", func() {
		storeHealthy()

		_, ok := cachedConnectionHealth(id, updatedAt.Add(time.Nanosecond))

		Expect(ok).To(BeFalse())
	})

	It("misses after the entry is explicitly forgotten", func() {
		storeHealthy()

		forgetConnectionHealth(id)

		_, ok := cachedConnectionHealth(id, updatedAt)
		Expect(ok).To(BeFalse())
	})

	It("never stores an indeterminate result", func() {
		storeConnectionHealth(ctx, connectionHealthResult{
			ID: id, State: connectionHealthUnknown, Detail: "budget expired",
			ConnectionUpdatedAt: updatedAt,
		})

		_, ok := cachedConnectionHealth(id, updatedAt)
		Expect(ok).To(BeFalse(), "an unfinished probe must not be mistaken for a verdict")
	})
})

var _ = Describe("connection health projections", func() {
	It("carries the probe's own timestamp and cache origin onto the info view", func() {
		checkedAt := time.Date(2026, 8, 13, 10, 31, 0, 0, time.UTC)

		info := connectionInfoFromHealth(connectionHealthResult{
			State:     connectionHealthHealthy,
			Server:    serverInfo{Status: "available", Product: "PostgreSQL", Version: "17.2"},
			Details:   connectionInfoDetails{Name: "warehouse", Type: "postgres"},
			CheckedAt: checkedAt,
			Cached:    true,
		})

		Expect(info.DiscoveredAt).To(Equal(checkedAt),
			"a cached view must report when the probe ran, not when it was served")
		Expect(info.Cached).To(BeTrue())
		Expect(info.Server.Version).To(Equal("17.2"))
		Expect(info.Connection.Name).To(Equal("warehouse"))
	})

	It("projects the dashboard summary without the resolved connection detail", func() {
		checkedAt := time.Date(2026, 8, 13, 10, 31, 0, 0, time.UTC)

		summary := healthSummary(connectionHealthResult{
			State: connectionHealthCredentials, Detail: "secret acme/db not found",
			Details:   connectionInfoDetails{ResolvedUsername: "operator"},
			CheckedAt: checkedAt, Cached: true,
		})

		Expect(summary).To(Equal(&connectionDashboardHealth{
			State:     connectionHealthCredentials,
			Detail:    "secret acme/db not found",
			CheckedAt: checkedAt,
			Cached:    true,
		}))
	})
})
