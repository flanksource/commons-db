// Tests the environment a caller's own git command gets to talk to a repository's remote: the
// connection's credentials scoped to the remote's host, and a defensive client's limits.
package git_test

import (
	"encoding/base64"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("A repository's command environment", func() {
	It("authenticates git to the remote's host with the connection's token", func(ctx SpecContext) {
		client, err := git.FromConnection(dbcontext.New(), models.Connection{Type: models.ConnectionTypeGithub, Password: "ghs_42",
			Properties: types.JSONStringMap{"organization": "acme"}})
		Expect(err).ToNot(HaveOccurred())
		repo, err := client.Repository("https://github.com/acme/api.git")
		Expect(err).ToNot(HaveOccurred())

		env, cleanup, err := repo.CommandEnv(ctx)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(cleanup)
		Expect(env).To(ContainElement(HaveSuffix("=http.https://github.com/.extraHeader")))
		Expect(env).To(ContainElement(HaveSuffix("=Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghs_42")))))
	})

	It("carries a defensive client's limits", func(ctx SpecContext) {
		client, err := git.FromConnection(dbcontext.New(), models.Connection{Name: "repo", Type: models.ConnectionTypeGit,
			URL: "https://127.0.0.1:9/acme/app.git", Password: "test-token"}, git.ClientOptions{Defensive: true})
		Expect(err).ToNot(HaveOccurred())
		repo, err := client.Repository("https://127.0.0.1:9/acme/app.git")
		Expect(err).ToNot(HaveOccurred())

		_, _, err = repo.CommandEnv(ctx)
		Expect(err).To(MatchError(ContainSubstring("refuses localhost address 127.0.0.1")))
	})
})
