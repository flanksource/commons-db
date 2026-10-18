package git_test

import (
	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func newRepository(rawURL string, auth git.Auth) *git.Repository {
	GinkgoHelper()
	repo, err := git.NewRepository(git.RepositoryOptions{URL: rawURL, Auth: auth})
	Expect(err).ToNot(HaveOccurred())
	return repo
}

var _ = Describe("Repository", func() {
	var fixture bareRepo
	BeforeEach(func() { fixture = newBareRepo("rules") })

	It("lists branches, tags and HEAD over file://", func(ctx SpecContext) {
		refs, err := newRepository(fixture.FileURL(), git.Auth{}).LsRemote(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(refs.Head).To(Equal(&git.Ref{Name: "main", FullName: "refs/heads/main", SHA: fixture.Main, Kind: git.RefBranch}))
		Expect(refs.Branches).To(ConsistOf(
			git.Ref{Name: "feature", FullName: "refs/heads/feature", SHA: fixture.Feature, Kind: git.RefBranch},
			git.Ref{Name: "main", FullName: "refs/heads/main", SHA: fixture.Main, Kind: git.RefBranch},
		))
		Expect(refs.Tags).To(ConsistOf(
			git.Ref{Name: "v1", FullName: "refs/tags/v1", SHA: fixture.First, Kind: git.RefTag, Annotated: true},
			git.Ref{Name: "v1-lite", FullName: "refs/tags/v1-lite", SHA: fixture.First, Kind: git.RefTag},
		))
	})

	DescribeTable("resolves refs",
		func(ctx SpecContext, name func(bareRepo) string, kind git.RefKind, sha func(bareRepo) string) {
			ref, err := newRepository(fixture.FileURL(), git.Auth{}).Resolve(ctx, name(fixture))
			Expect(err).ToNot(HaveOccurred())
			Expect(ref.Kind).To(Equal(kind))
			Expect(ref.SHA).To(Equal(sha(fixture)))
		},
		Entry("empty ref is the remote HEAD", func(bareRepo) string { return "" }, git.RefBranch, func(b bareRepo) string { return b.Main }),
		Entry("HEAD", func(bareRepo) string { return "HEAD" }, git.RefBranch, func(b bareRepo) string { return b.Main }),
		Entry("full ref", func(bareRepo) string { return "refs/heads/feature" }, git.RefBranch, func(b bareRepo) string { return b.Feature }),
		Entry("branch", func(bareRepo) string { return "feature" }, git.RefBranch, func(b bareRepo) string { return b.Feature }),
		Entry("annotated tag peels to its commit", func(bareRepo) string { return "v1" }, git.RefTag, func(b bareRepo) string { return b.First }),
		Entry("40-char SHA", func(b bareRepo) string { return b.First }, git.RefSHA, func(b bareRepo) string { return b.First }),
	)

	It("rejects short SHAs, unknown refs and branch/tag ambiguity", func(ctx SpecContext) {
		repo := newRepository(fixture.FileURL(), git.Auth{})
		_, err := repo.Resolve(ctx, fixture.First[:8])
		Expect(err).To(MatchError(ContainSubstring("short SHA")))
		_, err = repo.Resolve(ctx, "missing")
		Expect(err).To(MatchError(ContainSubstring(`ref "missing" not found`)))
		gitFixture(fixture.Path, "branch", "v1-lite", fixture.Main)
		_, err = repo.Resolve(ctx, "v1-lite")
		Expect(err).To(MatchError(ContainSubstring("ambiguous")))
	})

	It("lists a smart-HTTP remote only with Basic auth", func(ctx SpecContext) {
		remote := newHTTPRemote(fixture.Root, false)
		_, err := newRepository(remote.URL(fixture), git.Auth{}).LsRemote(ctx)
		Expect(err).To(HaveOccurred())
		refs, err := newRepository(remote.URL(fixture), git.Auth{Username: remote.User, Password: remote.Password}).LsRemote(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(refs.Head.SHA).To(Equal(fixture.Main))
	})

	It("verifies a TLS remote against the connection CA", func(ctx SpecContext) {
		remote := newHTTPRemote(fixture.Root, true)
		auth := git.Auth{Username: remote.User, Password: remote.Password}
		_, err := newRepository(remote.URL(fixture), auth).LsRemote(ctx)
		Expect(err).To(MatchError(ContainSubstring("certificate")))
		auth.TLS = git.TLS{CA: remote.CAPEM()}
		refs, err := newRepository(remote.URL(fixture), auth).LsRemote(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(refs.Head.SHA).To(Equal(fixture.Main))
	})

	It("rejects ambiguous credentials", func() {
		_, err := git.NewRepository(git.RepositoryOptions{URL: "https://example.com/a/b",
			Auth: git.Auth{Password: "p", Token: "t"}})
		Expect(err).To(MatchError(ContainSubstring("both a password and a token")))
	})
})
