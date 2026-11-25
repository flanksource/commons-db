package redis

import (
	"testing"
	"time"

	"github.com/flanksource/commons-db/lease"
	"github.com/flanksource/commons-db/lease/leasetest"
	redsyncredis "github.com/flanksource/commons-db/lease/redis/internal/redsync/redis"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/valkey-io/valkey-go"
	"github.com/valkey-io/valkey-go/valkeycompat"
)

func TestRedisLease(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Redis Lease Suite")
}

var engines = []struct{ name, env string }{{"Redis", "COMMONS_DB_LEASE_REDIS_URL"}, {"Valkey", "COMMONS_DB_LEASE_VALKEY_URL"}}

var _ = Describe("lease names", func() {
	It("names Redis keys by scope and key inside one hash tag", func() {
		Expect(redisKeys(lease.Request{Scope: "deploy", Key: "git-azure_oma"})).To(Equal([]string{
			"commons-db:lease:{deploy:git-azure_oma}:writer",
			"commons-db:lease:{deploy:git-azure_oma}:shared",
			"commons-db:lease:{deploy:git-azure_oma}:waiting",
			"commons-db:lease:{deploy:git-azure_oma}:grouped",
			"commons-db:lease:{deploy:git-azure_oma}:holders",
		}))
	})
})

var _ = Describe("live Redis lease backends", Label("integration"), func() {
	for _, engine := range engines {
		leasetest.LockerContract(engine.name, leasetest.AllModes, func(options lease.ExpiringOptions) (lease.Locker, error) {
			return New(newTestRedis(engine.env), options)
		})
		leasetest.HoldersContract(engine.name, leasetest.HolderBackend{NamesGroups: true, Lose: leasetest.ExpireUnrenewed, Factory: func(options lease.ExpiringOptions) (lease.Locker, error) {
			return New(newTestRedis(engine.env), options)
		}})
	}

	DescribeTable("rejects Redis lock operations from former writer tokens after expiry", func(ctx SpecContext, env string) {
		client := newTestRedis(env)
		backend := newRedisBackend(client, 3*time.Second)
		adapter, err := New(client, lease.ExpiringOptions{Options: lease.Options{Scope: "expiry-test"}, TTL: backend.ttl})
		Expect(err).NotTo(HaveOccurred())
		key := uuid.NewString()
		stale, err := backend.begin(lease.Request{Scope: "expiry-test", Key: key, Holder: "stale", Mode: lease.WriteMode}, false).Try(ctx)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func(ctx SpecContext) error { return stale.Unlock(ctx) })
		var next lease.Lease
		Eventually(func(g Gomega) bool {
			held, ok, err := adapter.Mutex(key, leasetest.As("next")).TryLock(ctx)
			g.Expect(err).NotTo(HaveOccurred())
			next = held
			return ok
		}, 5*time.Second, 50*time.Millisecond).Should(BeTrue())
		DeferCleanup(next.Unlock, ctx)
		Expect(stale.Renew(ctx)).To(MatchError(lease.ErrNotOwned))
		Expect(stale.Unlock(ctx)).To(Succeed())
		leasetest.ExpectBusy(adapter.Mutex(key, leasetest.As("contender")).TryLock(ctx))
	}, Entry("Redis", "COMMONS_DB_LEASE_REDIS_URL"), Entry("Valkey", "COMMONS_DB_LEASE_VALKEY_URL"))

	DescribeTable("expires the grouped index independently of plain readers", func(ctx SpecContext, env string) {
		client := newTestRedis(env)
		backend := newRedisBackend(client, 5*time.Second)
		key := uuid.NewString()
		cleanUp := func(held lease.Ownership) {
			DeferCleanup(func(ctx SpecContext) error { return held.Unlock(ctx) })
		}
		reader, err := backend.begin(lease.Request{Key: key, Mode: lease.ReadMode}, false).Try(ctx)
		Expect(err).NotTo(HaveOccurred())
		cleanUp(reader)

		usa := lease.Request{Key: key, Mode: lease.GroupMode, Group: "USA"}
		legacy := newRedisBackend(client, time.Second).begin(usa, false).(*redisAcquisition)
		// Simulate a grouped holder written before the index existed.
		now, err := valkeycompat.NewAdapter(client).Time(ctx).Result()
		Expect(err).NotTo(HaveOccurred())
		expires := now.Add(legacy.ttl)
		Expect(client.Do(ctx, client.B().Zadd().Key(legacy.keys[1]).ScoreMember().
			ScoreMember(float64(expires.UnixMilli()), legacy.share).Build()).Error()).To(Succeed())
		rsa := lease.Request{Key: key, Mode: lease.GroupMode, Group: "RSA"}
		_, err = backend.begin(rsa, false).Try(ctx)
		Expect(err).To(MatchError(lease.ErrBusy))
		peer, err := backend.begin(usa, false).Try(ctx)
		Expect(err).NotTo(HaveOccurred())
		cleanUp(peer)
		Expect(peer.Renew(ctx)).To(Succeed())
		Eventually(func(g Gomega) time.Time {
			now, err := valkeycompat.NewAdapter(client).Time(ctx).Result()
			g.Expect(err).NotTo(HaveOccurred())
			return now
		}, 2*time.Second, 50*time.Millisecond).Should(BeTemporally(">=", expires))
		Expect((&redisShared{legacy.redisResource, legacy.share}).Renew(ctx)).To(MatchError(lease.ErrNotOwned))
		_, err = backend.begin(rsa, false).Try(ctx)
		Expect(err).To(MatchError(lease.ErrBusy))
		Expect(peer.Unlock(ctx)).To(Succeed())
		other, err := backend.begin(rsa, false).Try(ctx)
		Expect(err).NotTo(HaveOccurred())
		cleanUp(other)
		Expect(reader.Renew(ctx)).To(Succeed())
	}, Entry("Redis", "COMMONS_DB_LEASE_REDIS_URL"), Entry("Valkey", "COMMONS_DB_LEASE_VALKEY_URL"))

	DescribeTable("passes caller-supplied Lua key counts", func(ctx SpecContext, env string) {
		conn, err := newRedisBackend(newTestRedis(env), leasetest.ContractTTL).pool.Get(ctx)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(conn.Close)
		script := redsyncredis.NewScript(-1, "return KEYS[1] .. ARGV[1]", "")
		Expect(conn.Eval(script, 1, "lock", "-token")).To(Equal("lock-token"))
		for _, args := range [][]any{nil, {-1}, {2, "lock"}, {"invalid"}, {1, 123}} {
			_, err := conn.Eval(script, args...)
			Expect(err).To(HaveOccurred())
		}
	}, Entry("Redis", "COMMONS_DB_LEASE_REDIS_URL"), Entry("Valkey", "COMMONS_DB_LEASE_VALKEY_URL"))
})

func newTestRedis(env string) valkey.Client {
	options, err := valkey.ParseURL(leasetest.TestSetting(env))
	Expect(err).NotTo(HaveOccurred())
	options.DisableCache = true
	client, err := valkey.NewClient(options)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(client.Close)
	return client
}
