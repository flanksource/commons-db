package git_test

import (
	"context"
	"path/filepath"
	"time"

	rpchttp "github.com/flanksource/clicky/rpc/http"
	"github.com/flanksource/commons-db/git"
	"github.com/gofrs/flock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("read preparation", func() {
	var (
		fixture bareRepo
		cache   *git.Cache
		repo    *git.Repository
	)
	BeforeEach(func() {
		fixture = newBareRepo("read-prep")
		cache = &git.Cache{Root: GinkgoT().TempDir()}
		repo = mirroredRepository(fixture)
	})
	mirrorLock := func() string { return filepath.Join(cache.Root, repo.URL.CacheKey(), "mirror.lock") }
	changes := func(ctx context.Context, base, head string) error {
		_, err := cache.Changes(ctx, git.ChangesOptions{Repository: repo, Base: base, Head: head})
		return err
	}
	counter := func(timings *rpchttp.Timings, metric string) int64 {
		value, _ := timings.Counter(metric, "ops")
		return value
	}

	It("spawns no presence check or pin update when it reads an already pinned commit again", func(ctx SpecContext) {
		Expect(changes(ctx, fixture.First, fixture.Main)).To(Succeed())
		timed, timings := rpchttp.WithTimings(ctx)
		Expect(changes(timed, fixture.First, fixture.Main)).To(Succeed())
		Expect(map[string]int64{
			"git_cat_file": counter(timings, "git_cat_file"), "git_update_ref": counter(timings, "git_update_ref"),
			"git_diff_tree": counter(timings, "git_diff_tree"), "git_fetch": counter(timings, "git_fetch"),
		}).To(Equal(map[string]int64{"git_cat_file": 0, "git_update_ref": 0, "git_diff_tree": 1, "git_fetch": 0}))
	})

	It("reads a pinned commit while another reader holds the mirror lock shared", func(ctx SpecContext) {
		Expect(changes(ctx, fixture.First, fixture.Main)).To(Succeed())
		reader := flock.New(mirrorLock())
		Expect(reader.TryRLock()).To(BeTrue())
		DeferCleanup(reader.Unlock)
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		Expect(changes(bounded, fixture.First, fixture.Main)).To(Succeed())
	})

	It("waits for the exclusive lock before fetching a commit the mirror lacks", func(ctx SpecContext) {
		Expect(changes(ctx, fixture.First, fixture.Main)).To(Succeed())
		reader := flock.New(mirrorLock())
		Expect(reader.TryRLock()).To(BeTrue())
		bounded, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		Expect(changes(bounded, fixture.Main, fixture.Feature)).To(MatchError(context.DeadlineExceeded))
		Expect(reader.Unlock()).To(Succeed())
		Expect(changes(ctx, fixture.Main, fixture.Feature)).To(Succeed())
	})

	It("resolves a full commit SHA without listing the remote", func(ctx SpecContext) {
		timed, timings := rpchttp.WithTimings(ctx)
		ref, err := repo.Resolve(timed, fixture.First)
		Expect(err).NotTo(HaveOccurred())
		Expect(ref).To(Equal(git.Ref{Name: fixture.First, FullName: fixture.First, SHA: fixture.First, Kind: git.RefSHA}))
		listed, _ := timings.Counter("git", "ls_remote")
		Expect(listed).To(BeZero())
	})
})
