// Tests auto-merging a pull request: Azure DevOps auto-complete, set as the connection's identity on
// the head that was reviewed, which completes the pull request once the base's policies pass.
package git_test

import (
	"net/http"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Auto-merging an Azure DevOps pull request", func() {
	const me = "8f1c2a8e-6b2d-4c1f-9a77-2f6f0b8d5e11"
	rules := func() git.RepoURL { return repoURL("https://dev.azure.com/acme/Platform/_git/rules", "") }
	autoMerger := func(api *fakeAPI) git.PullRequestAutoMerger {
		autoMerger, ok := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider.(git.PullRequestAutoMerger)
		Expect(ok).To(BeTrue())
		return autoMerger
	}
	pull := func(head string) func(*http.Request) apiReply {
		return reply(`{"pullRequestId":9,"status":"active","lastMergeSourceCommit":{"commitId":"` + head + `"}}`)
	}

	It("sets auto-complete as the connection's identity, with the strategy asked, keeping its policies and branch", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/_apis/connectionData":                                   reply(`{"authenticatedUser":{"id":"` + me + `"}}`),
			"GET /acme/Platform/_apis/git/repositories/rules/pullrequests/9":   pull(headSHA),
			"PATCH /acme/Platform/_apis/git/repositories/rules/pullrequests/9": reply(`{"pullRequestId":9,"status":"active","autoCompleteSetBy":{"id":"` + me + `"}}`),
		})
		Expect(autoMerger(api).AutoMergePullRequest(ctx, rules(), 9, git.MergePullRequestOptions{Method: git.MergeSquash, HeadSHA: headSHA})).To(Succeed())
		calls := api.Calls()
		Expect(calls).To(HaveLen(3))
		Expect(calls[2].Body).To(Equal(map[string]any{
			"autoCompleteSetBy": map[string]any{"id": me},
			"completionOptions": map[string]any{"mergeStrategy": "squash", "bypassPolicy": false, "deleteSourceBranch": false},
		}))
	})

	It("sets auto-complete with the commit message asked", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/_apis/connectionData":                                   reply(`{"authenticatedUser":{"id":"` + me + `"}}`),
			"GET /acme/Platform/_apis/git/repositories/rules/pullrequests/9":   pull(headSHA),
			"PATCH /acme/Platform/_apis/git/repositories/rules/pullrequests/9": reply(`{"pullRequestId":9,"status":"active","autoCompleteSetBy":{"id":"` + me + `"}}`),
		})
		options := git.MergePullRequestOptions{Method: git.MergeWithCommit, HeadSHA: headSHA, CommitMessage: "Tighten rules"}
		Expect(autoMerger(api).AutoMergePullRequest(ctx, rules(), 9, options)).To(Succeed())
		Expect(api.Calls()[2].Body["completionOptions"]).To(HaveKeyWithValue("mergeCommitMessage", "Tighten rules"))
	})

	It("refuses a pull request whose head moved since it was reviewed", func(ctx SpecContext) {
		const moved = "3333333333333333333333333333333333333333"
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/_apis/connectionData":                                 reply(`{"authenticatedUser":{"id":"` + me + `"}}`),
			"GET /acme/Platform/_apis/git/repositories/rules/pullrequests/9": pull(moved),
		})
		err := autoMerger(api).AutoMergePullRequest(ctx, rules(), 9, git.MergePullRequestOptions{Method: git.MergeWithCommit, HeadSHA: headSHA})
		Expect(err).To(MatchError("pull request 9's head is " + moved + ", not " + headSHA + ", the head that was reviewed"))
		for _, call := range api.Calls() {
			Expect(call.Method).NotTo(Equal(http.MethodPatch))
		}
	})

	It("reports auto-complete Azure didn't set", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/_apis/connectionData":                                   reply(`{"authenticatedUser":{"id":"` + me + `"}}`),
			"GET /acme/Platform/_apis/git/repositories/rules/pullrequests/9":   pull(headSHA),
			"PATCH /acme/Platform/_apis/git/repositories/rules/pullrequests/9": reply(`{"pullRequestId":9,"status":"active"}`),
		})
		err := autoMerger(api).AutoMergePullRequest(ctx, rules(), 9, git.MergePullRequestOptions{Method: git.MergeRebase, HeadSHA: headSHA})
		Expect(err).To(MatchError("azure DevOps didn't set auto-complete on pull request 9"))
	})

	It("needs the reviewed head and a strategy it knows", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{})
		Expect(autoMerger(api).AutoMergePullRequest(ctx, rules(), 9, git.MergePullRequestOptions{Method: git.MergeSquash})).
			To(MatchError("merging pull request 9 needs the head SHA that was reviewed"))
		Expect(api.Calls()).To(BeEmpty())
	})
})
