package git_test

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/lease"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/valkey-io/valkey-go"
)

// lostMaintenance hands out exclusive pool leases that are already lost, as a
// lease whose renewal failed would be, and delegates every other lock.
type lostMaintenance struct{ lease.Locker }

func (l lostMaintenance) ReadWriteLock(key string, options lease.LockOptions) lease.ReadWriteLock {
	return lostWriter{l.Locker.ReadWriteLock(key, options)}
}

type lostWriter struct{ lease.ReadWriteLock }

func (w lostWriter) Lock(ctx context.Context) (lease.Lease, error) {
	held, err := w.ReadWriteLock.Lock(ctx)
	if err != nil {
		return nil, err
	}
	return lostLease{held}, nil
}

type lostLease struct{ lease.Lease }

func (lostLease) Done() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

func (lostLease) Err() error { return lease.ErrLost }

func fileLocker() lease.Locker {
	GinkgoHelper()
	locks, err := lease.NewFile(fixtureTempDir(), lease.Options{Scope: "git-pool", RetryInterval: 20 * time.Millisecond})
	Expect(err).NotTo(HaveOccurred())
	return locks
}

func redisLocker() lease.Locker {
	GinkgoHelper()
	url := os.Getenv("COMMONS_DB_LEASE_REDIS_URL")
	if url == "" {
		Skip("set COMMONS_DB_LEASE_REDIS_URL to a disposable Redis")
	}
	options, err := valkey.ParseURL(url)
	Expect(err).NotTo(HaveOccurred())
	options.DisableCache = true
	client, err := valkey.NewClient(options)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(client.Close)
	locks, err := lease.NewRedis(client, lease.ExpiringOptions{Options: lease.Options{Scope: "git-pool-" + uuid.NewString(), RetryInterval: 20 * time.Millisecond}})
	Expect(err).NotTo(HaveOccurred())
	return locks
}

func poolRefs(pool string) []string {
	GinkgoHelper()
	return strings.Split(gitFixture(pool, "for-each-ref", "--format=%(refname) %(objectname)"), "\n")
}

func writeStamp(poolRoot, pool string, repo *git.Repository, used time.Time) {
	GinkgoHelper()
	dir := filepath.Join(poolRoot, pool, "stamps")
	Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(dir, repo.URL.CacheKey()), []byte(used.UTC().Format(time.RFC3339Nano)), 0o644)).To(Succeed())
}

func sharedPoolSpecs(backend string, newLocker func() lease.Locker) {
	Describe("shared object pool on "+backend, func() {
		var poolRoot, pool, poolName string
		var locks lease.Locker
		var a, b *git.Cache
		var donor, fork bareRepo
		var donorRepo, forkRepo *git.Repository
		var remote *httpRemote
		BeforeEach(func(ctx SpecContext) {
			locks = newLocker()
			donor, fork, remote = organizationFixtures()
			fork.Main = fork.commitOn(fixtureCommit{Branch: "main", Path: "fork.txt", Content: "fork\n", Message: "fork"})
			donorRepo, forkRepo = organizationRepository(remote, donor, "example"), organizationRepository(remote, fork, "example")
			poolRoot = fixtureTempDir()
			a = &git.Cache{Root: fixtureTempDir(), PoolRoot: poolRoot, Locker: locks, PoolRetain: time.Hour}
			b = &git.Cache{Root: fixtureTempDir(), PoolRoot: poolRoot, Locker: locks, PoolRetain: time.Hour}
			Expect(a.Fetch(ctx, donorRepo, git.FetchOptions{Refs: []git.Ref{branchRef("main")}})).To(Succeed())
			Expect(b.Fetch(ctx, forkRepo, git.FetchOptions{Refs: []git.Ref{branchRef("main")}})).To(Succeed())
			pools, err := filepath.Glob(filepath.Join(poolRoot, "*", "pool.git"))
			Expect(err).NotTo(HaveOccurred())
			Expect(pools).To(HaveLen(1))
			pool, poolName = pools[0], filepath.Base(filepath.Dir(pools[0]))
		})

		It("borrows from the shared pool through an absolute alternate", func() {
			for _, cache := range []*git.Cache{a, b} {
				Expect(filepath.Join(cache.Root, "v2", "pools")).NotTo(BeADirectory())
			}
			alternates, err := os.ReadFile(filepath.Join(b.Root, "v2", forkRepo.URL.CacheKey(), "mirror.git", "objects", "info", "alternates"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(alternates)).To(Equal(filepath.Join(pool, "objects") + "\n"))
		})

		It("never prunes on A a repository only B uses", func(ctx SpecContext) {
			Expect(a.Maintain(ctx, git.MaintainOptions{PinTTL: time.Hour})).To(Succeed())
			Expect(gitFixture(pool, "rev-parse", "refs/commons-pool/retained/"+forkRepo.URL.CacheKey()+"/"+fork.Main)).To(Equal(fork.Main))
			remote.Server.Close()
			data, err := b.Show(ctx, forkRepo, fork.Main, "fork.txt")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(Equal("fork\n"))
		})

		It("prunes on A a repository whose stamp is older than PoolRetain", func(ctx SpecContext) {
			writeStamp(poolRoot, poolName, forkRepo, time.Now().Add(-2*time.Hour))
			Expect(a.Maintain(ctx, git.MaintainOptions{PinTTL: time.Hour})).To(Succeed())
			refs := poolRefs(pool)
			Expect(refs).NotTo(ContainElement(ContainSubstring(forkRepo.URL.CacheKey())))
			Expect(refs).To(ContainElement(ContainSubstring(donorRepo.URL.CacheKey())))
			Expect(filepath.Join(poolRoot, poolName, "stamps", forkRepo.URL.CacheKey())).NotTo(BeAnExistingFile())
		})

		It("blocks A's maintenance while B holds a pool read lease", func(ctx SpecContext) {
			key := strings.ReplaceAll(poolName, ".", "_")
			read, err := locks.ReadWriteLock("pool-"+key[max(0, len(key)-88):], lease.LockOptions{Holder: "pod-b"}).RLock(ctx)
			Expect(err).NotTo(HaveOccurred())
			done := make(chan error, 1)
			go func() { done <- a.Maintain(ctx, git.MaintainOptions{PinTTL: time.Hour}) }()
			Consistently(done, 500*time.Millisecond).ShouldNot(Receive())
			Expect(read.Unlock(ctx)).To(Succeed())
			Eventually(ctx, done).WithTimeout(30 * time.Second).Should(Receive(BeNil()))
		})

		It("leaves the pool's refs and packs untouched when the maintenance lease is lost", func(ctx SpecContext) {
			writeStamp(poolRoot, poolName, forkRepo, time.Now().Add(-2*time.Hour))
			refs := poolRefs(pool)
			packs, err := filepath.Glob(filepath.Join(pool, "objects", "pack", "*"))
			Expect(err).NotTo(HaveOccurred())
			lost := &git.Cache{Root: a.Root, PoolRoot: poolRoot, Locker: lostMaintenance{locks}, PoolRetain: time.Hour}
			Expect(lost.Maintain(ctx, git.MaintainOptions{PinTTL: time.Hour})).To(MatchError(lease.ErrLost))
			Expect(poolRefs(pool)).To(Equal(refs))
			Expect(filepath.Glob(filepath.Join(pool, "objects", "pack", "*"))).To(Equal(packs))
		})

		It("refuses a mirror whose alternates point at a different pool", func(ctx SpecContext) {
			local := &git.Cache{Root: fixtureTempDir()}
			Expect(local.Fetch(ctx, donorRepo, git.FetchOptions{Refs: []git.Ref{branchRef("main")}})).To(Succeed())
			shared := &git.Cache{Root: local.Root, PoolRoot: poolRoot, Locker: locks}
			err := shared.Fetch(ctx, donorRepo, git.FetchOptions{Refs: []git.Ref{branchRef("main")}})
			Expect(err).To(MatchError(And(ContainSubstring(filepath.Join(local.Root, "v2", "pools")), ContainSubstring(filepath.Join(pool, "objects")))))
		})
	})
}

var _ = Describe("shared object pool", func() {
	sharedPoolSpecs("file leases", fileLocker)
	sharedPoolSpecs("redis leases", redisLocker)

	It("fails loudly when PoolRoot is set without a Locker", func(ctx SpecContext) {
		donor, _, remote := organizationFixtures()
		poolRoot := fixtureTempDir()
		cache := &git.Cache{Root: fixtureTempDir(), PoolRoot: poolRoot}
		err := cache.Fetch(ctx, organizationRepository(remote, donor, "example"), git.FetchOptions{Refs: []git.Ref{branchRef("main")}})
		Expect(err).To(MatchError("shared git pool " + poolRoot + " requires Cache.Locker"))
	})

	DescribeTable("transfers the shared base once across two caches fetching cold concurrently", func(ctx SpecContext, newLocker func() lease.Locker) {
		locks := newLocker()
		donor := newBareRepo("shared-cold-donor")
		payload := make([]byte, 512*1024)
		_, err := rand.New(rand.NewSource(13)).Read(payload)
		Expect(err).NotTo(HaveOccurred())
		donor.Main = donor.commitOn(fixtureCommit{Branch: "main", Path: "payload.bin", Content: string(payload), Message: "payload"})
		fork := newFork(donor)
		fork.Main = fork.commitOn(fixtureCommit{Branch: "main", Path: "delta.txt", Content: "delta\n", Message: "delta"})
		Expect(os.Symlink(donor.Path, filepath.Join(fork.Root, filepath.Base(donor.Path)))).To(Succeed())
		remote := newHTTPRemote(fork.Root, false)
		poolRoot := fixtureTempDir()
		before := remote.PackBytes()
		results := make(chan error, 2)
		for _, fixture := range []bareRepo{donor, fork} {
			cache := &git.Cache{Root: fixtureTempDir(), PoolRoot: poolRoot, Locker: locks}
			go func() {
				tree, err := cache.Checkout(ctx, organizationRepository(remote, fixture, "example"), git.CheckoutOptions{Ref: "main", Ephemeral: true})
				if err == nil {
					err = tree.Release()
				}
				results <- err
			}()
		}
		for range 2 {
			Eventually(ctx, results).WithTimeout(30 * time.Second).Should(Receive(BeNil()))
		}
		transferred := remote.PackBytes() - before
		Expect(transferred).To(BeNumerically(">", len(payload)))
		Expect(transferred).To(BeNumerically("<", len(payload)*3/2), "the second cache negotiated against the first one's pool tips")
	}, Entry("file leases", fileLocker), Entry("redis leases", redisLocker))
})
