package git_test

import (
	"encoding/base64"
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Clone", func() {
	var fixture bareRepo
	BeforeEach(func() { fixture = newBareRepo("rules") })

	It("clones the remote HEAD shallowly when no ref is given", func(ctx SpecContext) {
		dir := filepath.Join(GinkgoT().TempDir(), "checkout")
		ref, err := git.Clone(ctx, newRepository(fixture.FileURL(), git.Auth{}), git.CloneOptions{Dir: dir, Depth: 1})
		Expect(err).ToNot(HaveOccurred())
		Expect(ref.SHA).To(Equal(fixture.Main))
		Expect(ref.Name).To(Equal("main"))
		Expect(gitFixture(dir, "rev-list", "--count", "HEAD")).To(Equal("1"))
		Expect(gitFixture(dir, "symbolic-ref", "--short", "HEAD")).To(Equal("main"))
		Expect(os.ReadFile(filepath.Join(dir, "README.md"))).To(BeEquivalentTo("second\n"))
	})

	It("clones an annotated tag detached at its commit", func(ctx SpecContext) {
		dir := filepath.Join(GinkgoT().TempDir(), "checkout")
		ref, err := git.Clone(ctx, newRepository(fixture.FileURL(), git.Auth{}), git.CloneOptions{Dir: dir, Ref: "v1"})
		Expect(err).ToNot(HaveOccurred())
		Expect(ref.SHA).To(Equal(fixture.First))
		Expect(gitFixture(dir, "rev-parse", "HEAD")).To(Equal(fixture.First))
		Expect(os.ReadFile(filepath.Join(dir, "README.md"))).To(BeEquivalentTo("first\n"))
	})

	It("re-clones into an existing checkout, switching refs", func(ctx SpecContext) {
		dir := filepath.Join(GinkgoT().TempDir(), "checkout")
		repo := newRepository(fixture.FileURL(), git.Auth{})
		_, err := git.Clone(ctx, repo, git.CloneOptions{Dir: dir, Ref: "main", Depth: 1})
		Expect(err).ToNot(HaveOccurred())
		ref, err := git.Clone(ctx, repo, git.CloneOptions{Dir: dir, Ref: "feature", Depth: 1})
		Expect(err).ToNot(HaveOccurred())
		Expect(ref.SHA).To(Equal(fixture.Feature))
		Expect(gitFixture(dir, "rev-parse", "HEAD")).To(Equal(fixture.Feature))
	})

	It("refuses a non-empty directory that is not a repository", func(ctx SpecContext) {
		dir := GinkgoT().TempDir()
		writeFixtureFile(dir, "unrelated.txt", "x")
		_, err := git.Clone(ctx, newRepository(fixture.FileURL(), git.Auth{}), git.CloneOptions{Dir: dir})
		Expect(err).To(MatchError(ContainSubstring("not a git repository")))
	})

	It("authenticates over HTTP without leaking the secret into argv or config", func(ctx SpecContext) {
		argvLog := argvRecorder()
		remote := newHTTPRemote(fixture.Root, true)
		auth := git.Auth{Username: remote.User, Password: remote.Password, TLS: git.TLS{CA: remote.CAPEM()}}
		dir := filepath.Join(GinkgoT().TempDir(), "checkout")
		ref, err := git.Clone(ctx, newRepository(remote.URL(fixture), auth), git.CloneOptions{Dir: dir, Ref: "feature"})
		Expect(err).ToNot(HaveOccurred())
		Expect(ref.SHA).To(Equal(fixture.Feature))

		basic := base64.StdEncoding.EncodeToString([]byte(remote.User + ":" + remote.Password))
		Expect(remote.uploadPacks()).To(BeNumerically(">", 0))
		for _, request := range remote.Requests() {
			Expect(request.Authorization).To(Equal("Basic "+basic), "request %s %s", request.Method, request.Path)
		}
		argv, err := os.ReadFile(argvLog)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(argv)).To(ContainSubstring("fetch"))
		config, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
		Expect(err).ToNot(HaveOccurred())
		for _, leaked := range []string{string(argv), string(config)} {
			Expect(leaked).ToNot(ContainSubstring(remote.Password))
			Expect(leaked).ToNot(ContainSubstring(basic))
		}
	})
})
