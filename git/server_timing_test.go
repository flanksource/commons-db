package git_test

import (
	"context"
	"path/filepath"

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
})
