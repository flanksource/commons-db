// Tests approving a pull request as the identity the connection authenticates as, leaving an
// optional comment, and refusing a vote the provider didn't record.
package git_test

import (
	"net/http"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Approving an Azure DevOps pull request", func() {
	const me = "8f1c2a8e-6b2d-4c1f-9a77-2f6f0b8d5e11"
	rules := func() git.RepoURL { return repoURL("https://dev.azure.com/acme/Platform/_git/rules", "") }
	approver := func(api *fakeAPI) git.PullRequestApprover {
		approver, ok := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider.(git.PullRequestApprover)
		Expect(ok).To(BeTrue())
		return approver
	}

	It("votes approve as the connection's identity and leaves the comment as a thread", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/_apis/connectionData": reply(`{"authenticatedUser":{"id":"` + me + `","providerDisplayName":"Ada Lovelace"}}`),
			"PUT /acme/Platform/_apis/git/repositories/rules/pullrequests/9/reviewers/" + me: reply(`{"id":"` + me + `","vote":10}`),
			"GET /acme/Platform/_apis/git/repositories/rules/pullrequests/9": reply(`{"pullRequestId":9,"status":"active","sourceRefName":"refs/heads/a",` +
				`"targetRefName":"refs/heads/main","repository":{"name":"rules","project":{"name":"Platform"}}}`),
			"POST /acme/Platform/_apis/git/repositories/rules/pullrequests/9/threads": reply(`{"id":5}`),
		})
		Expect(approver(api).ApprovePullRequest(ctx, rules(), 9, "Ship it.")).To(Succeed())
		calls := api.Calls()
		Expect(calls).To(HaveLen(4))
		Expect(calls[1].Body).To(Equal(map[string]any{"vote": float64(10)}))
		Expect(calls[3].Body).To(HaveKeyWithValue("status", "active"))
	})

	It("leaves no thread without a comment", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/_apis/connectionData": reply(`{"authenticatedUser":{"id":"` + me + `"}}`),
			"PUT /acme/Platform/_apis/git/repositories/rules/pullrequests/9/reviewers/" + me: reply(`{"id":"` + me + `","vote":10}`),
		})
		Expect(approver(api).ApprovePullRequest(ctx, rules(), 9, "")).To(Succeed())
		Expect(api.Calls()).To(HaveLen(2))
	})

	It("reports a vote Azure didn't record", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/_apis/connectionData": reply(`{"authenticatedUser":{"id":"` + me + `"}}`),
			"PUT /acme/Platform/_apis/git/repositories/rules/pullrequests/9/reviewers/" + me: reply(`{"id":"` + me + `","vote":0}`),
		})
		Expect(approver(api).ApprovePullRequest(ctx, rules(), 9, "")).
			To(MatchError("azure DevOps recorded vote 0 instead of approving pull request 9"))
	})

	It("needs the identity the connection authenticates as", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/_apis/connectionData": reply(`{"authenticatedUser":{}}`),
		})
		Expect(approver(api).ApprovePullRequest(ctx, rules(), 9, "")).
			To(MatchError(ContainSubstring("azure DevOps named no identity for the connection to approve as")))
	})
})
