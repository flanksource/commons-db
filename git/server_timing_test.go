package git_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	rpchttp "github.com/flanksource/clicky/rpc/http"
	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Git server timing", func() {
	It("records remote ref operations and go-git TLS connection phases", func() {
		bare := newBareRepo("timed")
		remote := newHTTPRemote(filepath.Dir(bare.Path), true)
		DeferCleanup(remote.Server.Close)
		repo := newRepository(remote.URL(bare), git.Auth{Username: remote.User, Password: remote.Password, TLS: git.TLS{CA: remote.CAPEM()}})
		ctx, timings := rpchttp.WithTimings(context.Background())
		_, err := repo.LsRemote(ctx)
		Expect(err).NotTo(HaveOccurred())
		ops, _ := timings.Counter("git", "ls_remote")
		Expect(ops).To(Equal(int64(1)))
		handshakes, _ := timings.Counter("http_tls", "ops")
		Expect(handshakes).To(Equal(int64(1)))
	})

	It("counts failed remote operations and preserves the error", func() {
		repo := newRepository("file://"+filepath.Join(GinkgoT().TempDir(), "missing.git"), git.Auth{})
		ctx, timings := rpchttp.WithTimings(context.Background())
		_, err := repo.LsRemote(ctx)
		Expect(err).To(HaveOccurred())
		failures, _ := timings.Counter("git", "errors")
		Expect(failures).To(Equal(int64(1)))
	})

	It("records subprocess operations while leaving unrelated request collectors empty", func() {
		bare := newBareRepo("cli-timed")
		repo := newRepository(bare.FileURL(), git.Auth{})
		ctx, timings := rpchttp.WithTimings(context.Background())
		_, other := rpchttp.WithTimings(context.Background())
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		worktree, err := cache.Checkout(ctx, repo, git.CheckoutOptions{Ref: "main"})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(worktree.Release)
		commands, _ := timings.Counter("git", "commands")
		Expect(commands).To(BeNumerically(">", 0))
		Expect(other.Header()).To(BeEmpty())
	})

	It("attributes subprocess resource usage to the git subcommand", func() {
		bare := newBareRepo("rusage-timed")
		repo := newRepository(bare.FileURL(), git.Auth{})
		ctx, timings := rpchttp.WithTimings(context.Background())
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		worktree, err := cache.Checkout(ctx, repo, git.CheckoutOptions{Ref: "main"})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(worktree.Release)
		for _, counter := range []string{"cpu_user_us", "cpu_sys_us", "in_blocks", "out_blocks", "max_rss_bytes"} {
			_, recorded := timings.Counter("git", counter)
			Expect(recorded).To(BeTrue(), counter)
		}
		worktreeOps, _ := timings.Counter("git_worktree", "ops")
		Expect(worktreeOps).To(BeNumerically(">=", 1))
		_, recorded := timings.Counter("git_worktree", "cpu_user_us")
		Expect(recorded).To(BeTrue())
		for _, metric := range timings.Metrics() {
			for _, counter := range metric.Counters {
				if counter.Name == "max_rss_bytes" {
					Expect(counter.Aggregate).To(Equal(rpchttp.AggregateMax), metric.Name)
				}
			}
		}
	})

	It("records the bytes a fetch received when progress is observed", func() {
		// Over fetch.unpackLimit (100 objects) git indexes the pack and reports
		// the bytes it received; smaller packs unpack without a byte count.
		root := fixtureTempDir()
		work := filepath.Join(root, "work")
		gitFixture(root, "init", "-q", "-b", "main", work)
		gitFixture(work, "config", "maintenance.autoDetach", "false")
		for i := range 150 {
			writeFixtureFile(work, fmt.Sprintf("rules/rule-%03d.xml", i), strings.Repeat(fmt.Sprintf("<Rule id=\"%d\"/>\n", i), 64))
		}
		gitFixture(work, "add", "-A")
		gitFixture(work, "commit", "-q", "-m", "rules")
		repo := newRepository("file://"+work, git.Auth{})
		ctx, timings := rpchttp.WithTimings(context.Background())
		ctx = git.WithFetchObserver(ctx, func(git.FetchProgress) {})
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		worktree, err := cache.Checkout(ctx, repo, git.CheckoutOptions{Ref: "main"})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(worktree.Release)
		received, recorded := timings.Counter("git_fetch", "received_kib")
		Expect(recorded).To(BeTrue())
		Expect(received).To(BeNumerically(">", 0))
	})

	It("does not attribute go-git remote listing to a subprocess", func() {
		bare := newBareRepo("rusage-remote")
		repo := mirroredRepository(bare)
		ctx, timings := rpchttp.WithTimings(context.Background())
		_, err := repo.LsRemote(ctx)
		Expect(err).NotTo(HaveOccurred())
		_, recorded := timings.Counter("git", "cpu_user_us")
		Expect(recorded).To(BeFalse())
	})
})
