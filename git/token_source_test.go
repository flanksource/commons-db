// Tests a provider client authenticating its API calls with a caller's access token source, such
// as an az CLI login, in place of the connection's personal access token.
package git_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("A provider client with a token source", func() {
	It("asks the source for a bearer token on every API call, with no personal access token", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/_apis/projects": reply(`{"value":[]}`),
		})
		minted := 0
		client, err := git.FromConnection(dbcontext.New(), models.Connection{Name: "acme", Type: models.ConnectionTypeAzureDevops,
			URL: api.Server.URL + "/acme", InsecureTLS: true}, git.ClientOptions{TokenSource: func(context.Context) (string, error) {
			minted++
			return fmt.Sprintf("entra-%d", minted), nil
		}})
		Expect(err).ToNot(HaveOccurred())

		Expect(client.Provider.Verify(ctx)).To(Succeed())
		Expect(client.Provider.Verify(ctx)).To(Succeed())
		calls := api.Calls()
		Expect(calls).To(HaveLen(2))
		Expect(calls[0].Authorization).To(Equal("Bearer entra-1"))
		Expect(calls[1].Authorization).To(Equal("Bearer entra-2"), "a refreshed token is used as soon as the source hands it out")
	})

	It("reports a source that has no token, without calling the API", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{"GET /acme/_apis/projects": reply(`{"value":[]}`)})
		client, err := git.FromConnection(dbcontext.New(), models.Connection{Name: "acme", Type: models.ConnectionTypeAzureDevops,
			URL: api.Server.URL + "/acme", InsecureTLS: true}, git.ClientOptions{TokenSource: func(context.Context) (string, error) {
			return "", errors.New("run az login")
		}})
		Expect(err).ToNot(HaveOccurred())

		Expect(client.Provider.Verify(ctx)).To(MatchError(ContainSubstring("run az login")))
		Expect(api.Calls()).To(BeEmpty())
	})
})
