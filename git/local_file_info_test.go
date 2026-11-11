package git_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	rpchttp "github.com/flanksource/clicky/rpc/http"
	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/git/gittest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Local file provenance", func() {
	It("caches committed provenance while reading literal working-tree status on every call", func(ctx SpecContext) {
		root := GinkgoT().TempDir()
		const path = "[Rule].xml"
		const committedAt = "2024-04-12T10:30:00Z"
		Expect(gittest.Init(ctx, root)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, path), []byte("<Original/>"), 0o600)).To(Succeed())
		_, err := gittest.Run(ctx, gittest.Options{Directory: root, Args: []string{"add", "--", path}})
		Expect(err).NotTo(HaveOccurred())
		_, err = gittest.Run(ctx, gittest.Options{Directory: root, Args: []string{"commit", "--quiet", "-m", "Add rule"}, Environment: []string{"GIT_AUTHOR_DATE=2023-01-01T00:00:00Z", "GIT_COMMITTER_DATE=" + committedAt}})
		Expect(err).NotTo(HaveOccurred())
		sha, err := git.Head(ctx, root)
		Expect(err).NotTo(HaveOccurred())
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		options := git.LocalFileOptions{Directory: root, Revision: sha, Path: path}
		first, err := cache.LocalFile(ctx, options)
		Expect(err).NotTo(HaveOccurred())
		date, err := time.Parse(time.RFC3339, committedAt)
		Expect(err).NotTo(HaveOccurred())
		Expect(first).To(Equal(git.LocalFileInfo{Commit: git.FileCommit{SHA: strings.TrimSpace(sha), CommittedAt: date, Present: true}}))
		Expect(os.WriteFile(filepath.Join(root, path), []byte("<Changed/>"), 0o600)).To(Succeed())
		timed, timings := rpchttp.WithTimings(ctx)
		second, err := cache.LocalFile(timed, options)
		Expect(err).NotTo(HaveOccurred())
		Expect(second).To(Equal(git.LocalFileInfo{Commit: first.Commit, Dirty: true}))
		logs, _ := timings.Counter("git_log", "ops")
		Expect(logs).To(BeZero())
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, err = cache.LocalFile(canceled, options)
		Expect(err).To(MatchError(context.Canceled))
	})
})
