// Tests a defensive git client, for credentials someone other than the operator supplied: it
// reaches provider APIs and git remotes on public addresses only, with TLS verified.
package git_test

import (
	"strings"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("A defensive git client", func() {
	defensive := git.ClientOptions{Defensive: true}

	It("refuses a provider API on a loopback address before sending anything", func(ctx SpecContext) {
		api := newFakeAPI(nil)
		client, err := git.FromConnection(dbcontext.New(), models.Connection{Type: models.ConnectionTypeGithub, URL: api.Server.URL,
			Password: "test-token", Properties: types.JSONStringMap{"organization": "acme"}}, defensive)
		Expect(err).ToNot(HaveOccurred())
		_, err = client.Provider.ListRepositories(ctx, git.ListRepositoriesOptions{})
		Expect(err).To(MatchError(ContainSubstring("refuses localhost address 127.0.0.1")))
		Expect(api.Calls()).To(BeEmpty())
	})

	It("refuses a connection that skips TLS verification", func() {
		_, err := git.FromConnection(dbcontext.New(), models.Connection{Name: "lax", Type: models.ConnectionTypeGitlab, URL: "https://gitlab.example.com",
			Password: "test-token", InsecureTLS: true}, defensive)
		Expect(err).To(MatchError(`connection "lax" skips TLS verification, which a defensive client refuses`))
	})

	DescribeTable("refuses a git remote it may not reach",
		func(ctx SpecContext, remote, refused string) {
			client, err := git.FromConnection(dbcontext.New(), models.Connection{Name: "repo", Type: models.ConnectionTypeGit, URL: remote, Password: "test-token"}, defensive)
			Expect(err).ToNot(HaveOccurred())
			repo, err := client.Repository(remote)
			Expect(err).ToNot(HaveOccurred())

			_, err = repo.LsRemote(ctx)
			Expect(err).To(MatchError(ContainSubstring(refused)), "ls-remote")
			_, err = git.Clone(ctx, repo, git.CloneOptions{Dir: GinkgoT().TempDir(), Ref: strings.Repeat("a", 40)})
			Expect(err).To(MatchError(ContainSubstring(refused)), "fetch")
		},
		Entry("on a loopback address", "https://127.0.0.1:9/acme/app.git", "refuses localhost address 127.0.0.1"),
		Entry("on a private address", "https://10.0.0.5/acme/app.git", "refuses private address 10.0.0.5"),
		Entry("on the cloud metadata address", "https://169.254.169.254/acme/app.git", "refuses reserved address 169.254.169.254"),
		Entry("over plain http", "http://git.example.com/acme/app.git", "a defensive client reaches git remotes over https or ssh only, not http"),
	)
})
