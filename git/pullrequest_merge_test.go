// Tests completing a pull request: merging the head that was reviewed with the strategy asked,
// through the target's policies, and reporting a completion the provider didn't make.
package git_test

import (
	"net/http"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Merging an Azure DevOps pull request", func() {
	rules := func() git.RepoURL { return repoURL("https://dev.azure.com/acme/Platform/_git/rules", "") }
	merger := func(api *fakeAPI) git.PullRequestMerger {
		merger, ok := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider.(git.PullRequestMerger)
		Expect(ok).To(BeTrue())
		return merger
	}

	It("completes the reviewed head with the strategy asked, keeping its policies and branch", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"PATCH /acme/Platform/_apis/git/repositories/rules/pullrequests/9": reply(`{"pullRequestId":9,"status":"completed"}`),
		})
		Expect(merger(api).MergePullRequest(ctx, rules(), 9, git.MergePullRequestOptions{Method: git.MergeSquash, HeadSHA: headSHA})).To(Succeed())
		Expect(api.Calls()[0].Body).To(Equal(map[string]any{
			"status":                "completed",
			"lastMergeSourceCommit": map[string]any{"commitId": headSHA},
			"completionOptions":     map[string]any{"mergeStrategy": "squash", "bypassPolicy": false, "deleteSourceBranch": false},
		}))
	})

	It("accepts a completion Azure queued behind its policies", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"PATCH /acme/Platform/_apis/git/repositories/rules/pullrequests/9": reply(`{"pullRequestId":9,"status":"active","completionQueueTime":"2026-11-02T12:00:00Z"}`),
		})
		Expect(merger(api).MergePullRequest(ctx, rules(), 9, git.MergePullRequestOptions{Method: git.MergeCommit, HeadSHA: headSHA})).To(Succeed())
		Expect(api.Calls()[0].Body["completionOptions"]).To(HaveKeyWithValue("mergeStrategy", "noFastForward"))
	})

	It("reports a completion Azure refused", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"PATCH /acme/Platform/_apis/git/repositories/rules/pullrequests/9": reply(`{"pullRequestId":9,"status":"active","completionQueueTime":"2026-11-02T12:00:00Z",
				"mergeStatus":"conflicts","mergeFailureMessage":"merge conflicts in rules.go"}`),
		})
		err := merger(api).MergePullRequest(ctx, rules(), 9, git.MergePullRequestOptions{Method: git.MergeRebase, HeadSHA: headSHA})
		Expect(err).To(MatchError(`azure DevOps did not complete pull request 9: status "active", merge status "conflicts": merge conflicts in rules.go`))
	})

	It("needs the reviewed head and a strategy it knows", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{})
		Expect(merger(api).MergePullRequest(ctx, rules(), 9, git.MergePullRequestOptions{Method: git.MergeSquash})).
			To(MatchError("merging pull request 9 needs the head SHA that was reviewed"))
		Expect(merger(api).MergePullRequest(ctx, rules(), 9, git.MergePullRequestOptions{Method: "octopus", HeadSHA: headSHA})).
			To(MatchError(`merge method "octopus" is not merge, squash or rebase`))
		Expect(api.Calls()).To(BeEmpty())
	})
})
