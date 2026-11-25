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

	It("completes with the commit message asked, except on a rebase, which keeps its commits", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"PATCH /acme/Platform/_apis/git/repositories/rules/pullrequests/9": reply(`{"pullRequestId":9,"status":"completed"}`),
		})
		options := git.MergePullRequestOptions{Method: git.MergeSquash, HeadSHA: headSHA, CommitMessage: "Tighten rules\n\nWhy they change"}
		Expect(merger(api).MergePullRequest(ctx, rules(), 9, options)).To(Succeed())
		options.Method = git.MergeRebase
		Expect(merger(api).MergePullRequest(ctx, rules(), 9, options)).To(Succeed())
		Expect(api.Calls()[0].Body["completionOptions"]).To(HaveKeyWithValue("mergeCommitMessage", "Tighten rules\n\nWhy they change"))
		Expect(api.Calls()[1].Body["completionOptions"]).NotTo(HaveKey("mergeCommitMessage"))
	})

	It("accepts a completion Azure queued behind its policies", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"PATCH /acme/Platform/_apis/git/repositories/rules/pullrequests/9": reply(`{"pullRequestId":9,"status":"active","completionQueueTime":"2026-11-02T12:00:00Z"}`),
		})
		Expect(merger(api).MergePullRequest(ctx, rules(), 9, git.MergePullRequestOptions{Method: git.MergeWithCommit, HeadSHA: headSHA})).To(Succeed())
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

var _ = Describe("Merging a GitLab merge request", func() {
	const merge = "/api/v4/projects/acme%2Fapi/merge_requests/5/merge"
	api := func() git.RepoURL {
		return repoURL("https://gitlab.example.com/acme/api.git", models.ConnectionTypeGitlab)
	}
	merger := func(fake *fakeAPI) git.PullRequestMerger {
		merger, ok := providerClient(models.ConnectionTypeGitlab, fake.Server.URL, nil).Provider.(git.PullRequestMerger)
		Expect(ok).To(BeTrue())
		return merger
	}

	It("merges the reviewed head, squashed when asked, keeping its source branch", func(ctx SpecContext) {
		fake := newFakeAPI(map[string]func(*http.Request) apiReply{"PUT " + merge: reply(`{"iid":5,"state":"merged"}`)})
		Expect(merger(fake).MergePullRequest(ctx, api(), 5, git.MergePullRequestOptions{Method: git.MergeSquash, HeadSHA: headSHA})).To(Succeed())
		Expect(fake.Calls()[0].Body).To(Equal(map[string]any{"sha": headSHA, "squash": true, "should_remove_source_branch": false}))
		Expect(merger(fake).MergePullRequest(ctx, api(), 5, git.MergePullRequestOptions{Method: git.MergeWithCommit, HeadSHA: headSHA})).To(Succeed())
		Expect(fake.Calls()[1].Body).To(HaveKeyWithValue("squash", false))
	})

	It("merges with the commit message asked, as the squash commit's when squashed", func(ctx SpecContext) {
		fake := newFakeAPI(map[string]func(*http.Request) apiReply{"PUT " + merge: reply(`{"iid":5,"state":"merged"}`)})
		options := git.MergePullRequestOptions{Method: git.MergeWithCommit, HeadSHA: headSHA, CommitMessage: "Tighten rules"}
		Expect(merger(fake).MergePullRequest(ctx, api(), 5, options)).To(Succeed())
		options.Method = git.MergeSquash
		Expect(merger(fake).MergePullRequest(ctx, api(), 5, options)).To(Succeed())
		Expect(fake.Calls()[0].Body).To(HaveKeyWithValue("merge_commit_message", "Tighten rules"))
		Expect(fake.Calls()[0].Body).NotTo(HaveKey("squash_commit_message"))
		Expect(fake.Calls()[1].Body).To(HaveKeyWithValue("squash_commit_message", "Tighten rules"))
		Expect(fake.Calls()[1].Body).NotTo(HaveKey("merge_commit_message"))
	})

	It("refuses a rebase, which GitLab doesn't merge in one request", func(ctx SpecContext) {
		fake := newFakeAPI(map[string]func(*http.Request) apiReply{})
		err := merger(fake).MergePullRequest(ctx, api(), 5, git.MergePullRequestOptions{Method: git.MergeRebase, HeadSHA: headSHA})
		Expect(err).To(MatchError("gitlab merges merge request !5 with merge or squash; rebase it on GitLab, then merge it"))
		Expect(fake.Calls()).To(BeEmpty())
	})

	It("reports a merge GitLab didn't make", func(ctx SpecContext) {
		fake := newFakeAPI(map[string]func(*http.Request) apiReply{"PUT " + merge: reply(`{"iid":5,"state":"opened"}`)})
		err := merger(fake).MergePullRequest(ctx, api(), 5, git.MergePullRequestOptions{Method: git.MergeWithCommit, HeadSHA: headSHA})
		Expect(err).To(MatchError(`gitlab left merge request !5 opened instead of merging it`))
	})
})
