package git_test

import (
	"context"
	"net/http"
	"time"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const headSHA = "0123456789abcdef0123456789abcdef01234567"

var applyStatus = git.CommitStatus{State: git.CommitStateFailure, Context: "oipa-cli/apply",
	Description: "2 steps failed", TargetURL: "https://mc.example.com/runs/1"}

var _ = Describe("GitHub pull request head, status and comment", func() {
	It("reads the fork head, posts a status and returns the comment URL", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /repos/acme/widgets/pulls/5":              reply(`{"number":5,"html_url":"u","state":"open","title":"T","head":{"ref":"apply","sha":"` + headSHA + `","repo":{"full_name":"fork/widgets"}},"base":{"ref":"main"}}`),
			"POST /repos/acme/widgets/statuses/" + headSHA: reply(`{}`),
			"POST /repos/acme/widgets/issues/5/comments":   reply(`{"id":77,"html_url":"https://github.com/acme/widgets/pull/5#issuecomment-77"}`),
		})
		provider := providerClient(models.ConnectionTypeGithub, api.Server.URL, nil).Provider
		repo := repoURL("https://github.com/acme/widgets", "")

		pr, err := provider.GetPullRequest(ctx, repo, 5)
		Expect(err).ToNot(HaveOccurred())
		Expect(pr).To(Equal(git.PullRequest{Number: 5, URL: "u", State: git.PullRequestOpen, Head: "apply", Base: "main",
			Title: "T", HeadSHA: headSHA, HeadRepository: "fork/widgets"}))
		Expect(provider.SetCommitStatus(ctx, repo, headSHA, applyStatus)).To(Succeed())
		commentURL, err := provider.CommentPullRequest(ctx, repo, 5, "run failed")
		Expect(err).ToNot(HaveOccurred())
		Expect(commentURL).To(Equal("https://github.com/acme/widgets/pull/5#issuecomment-77"))

		calls := api.Calls()
		Expect(calls[1].Body).To(Equal(map[string]any{"state": "failure", "context": "oipa-cli/apply",
			"description": "2 steps failed", "target_url": "https://mc.example.com/runs/1"}))
		Expect(calls[2].Body).To(Equal(map[string]any{"body": "run failed"}))
	})
})

var _ = Describe("GitLab merge request head, status and comment", func() {
	mrPath := "/api/v4/projects/acme%2Fcli/merge_requests/3"

	It("resolves a fork source project, maps failure to failed and builds the note URL", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET " + mrPath:           reply(`{"iid":3,"web_url":"https://gl/acme/cli/-/merge_requests/3","state":"opened","title":"T","source_branch":"apply","target_branch":"main","sha":"` + headSHA + `","source_project_id":42,"target_project_id":7}`),
			"GET /api/v4/projects/42": reply(`{"path":"cli","path_with_namespace":"fork/cli"}`),
			"POST /api/v4/projects/acme%2Fcli/statuses/" + headSHA: reply(`{}`),
			"POST " + mrPath + "/notes":                            reply(`{"id":901}`),
		})
		provider := providerClient(models.ConnectionTypeGitlab, api.Server.URL, nil).Provider
		repo := repoURL("https://gitlab.example.com/acme/cli.git", models.ConnectionTypeGitlab)

		pr, err := provider.GetPullRequest(ctx, repo, 3)
		Expect(err).ToNot(HaveOccurred())
		Expect([]string{pr.HeadSHA, pr.HeadRepository}).To(Equal([]string{headSHA, "fork/cli"}))
		Expect(provider.SetCommitStatus(ctx, repo, headSHA, applyStatus)).To(Succeed())
		noteURL, err := provider.CommentPullRequest(ctx, repo, 3, "run failed")
		Expect(err).ToNot(HaveOccurred())
		Expect(noteURL).To(Equal("https://gl/acme/cli/-/merge_requests/3#note_901"))

		calls := api.Calls()
		Expect(calls[2].Body).To(Equal(map[string]any{"state": "failed", "name": "oipa-cli/apply",
			"description": "2 steps failed", "target_url": "https://mc.example.com/runs/1"}))
		Expect(calls[4].Body).To(Equal(map[string]any{"body": "run failed"}))
	})

	It("names the target project as the head repository of a same-project merge request", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET " + mrPath: reply(`{"iid":3,"web_url":"u","state":"opened","title":"T","source_branch":"apply","target_branch":"main","sha":"` + headSHA + `","source_project_id":7,"target_project_id":7}`),
		})
		provider := providerClient(models.ConnectionTypeGitlab, api.Server.URL, nil).Provider
		pr, err := provider.GetPullRequest(ctx, repoURL("https://gitlab.example.com/acme/cli.git", models.ConnectionTypeGitlab), 3)
		Expect(err).ToNot(HaveOccurred())
		Expect(pr.HeadRepository).To(Equal("acme/cli"))
		Expect(api.Calls()).To(HaveLen(1))
	})
})

var _ = Describe("Azure DevOps pull request head, status and comment", func() {
	repoPath := "/acme/Platform/_apis/git/repositories/rules"
	pull := `{"pullRequestId":9,"status":"active","title":"T","sourceRefName":"refs/heads/apply","targetRefName":"refs/heads/main",` +
		`"lastMergeSourceCommit":{"commitId":"` + headSHA + `"},"forkSource":{"repository":{"name":"rules-fork","project":{"name":"Forks"}}},` +
		`"repository":{"name":"rules","project":{"name":"Platform"},"webUrl":"https://dev.azure.com/acme/Platform/_git/rules"}}`

	It("reads the fork head, posts a split-context status and links the comment thread", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET " + repoPath + "/pullrequests/9":                    reply(pull),
			"POST " + repoPath + "/commits/" + headSHA + "/statuses": reply(`{}`),
			"POST " + repoPath + "/pullrequests/9/threads":           reply(`{"id":31}`),
		})
		provider := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider
		repo := repoURL("https://dev.azure.com/acme/Platform/_git/rules", "")

		pr, err := provider.GetPullRequest(ctx, repo, 9)
		Expect(err).ToNot(HaveOccurred())
		Expect([]string{pr.HeadSHA, pr.HeadRepository}).To(Equal([]string{headSHA, "Forks/rules-fork"}))
		Expect(provider.SetCommitStatus(ctx, repo, headSHA, applyStatus)).To(Succeed())
		threadURL, err := provider.CommentPullRequest(ctx, repo, 9, "run failed")
		Expect(err).ToNot(HaveOccurred())
		Expect(threadURL).To(Equal("https://dev.azure.com/acme/Platform/_git/rules/pullrequest/9?discussionId=31"))

		calls := api.Calls()
		Expect(calls[1].Query).To(Equal("api-version=7.1"))
		Expect(calls[1].Body).To(Equal(map[string]any{"state": "failed", "description": "2 steps failed",
			"targetUrl": "https://mc.example.com/runs/1", "context": map[string]any{"genre": "oipa-cli", "name": "apply"}}))
		Expect(calls[3].Body).To(Equal(map[string]any{"status": "active",
			"comments": []any{map[string]any{"parentCommentId": 0.0, "content": "run failed", "commentType": "text"}}}))
	})
})

var _ = DescribeTable("maps a provider-neutral commit state to the provider's",
	func(ctx SpecContext, connectionType, connectionPath, repo, statusPath string, state git.CommitState, want string) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{"POST " + statusPath: reply(`{}`)})
		provider := providerClient(connectionType, api.Server.URL+connectionPath, nil).Provider
		Expect(provider.SetCommitStatus(ctx, repoURL(repo, connectionType), headSHA,
			git.CommitStatus{State: state, Context: "ci"})).To(Succeed())
		Expect(api.Calls()[0].Body["state"]).To(Equal(want))
	},
	Entry("github error", models.ConnectionTypeGithub, "", "https://github.com/acme/widgets", "/repos/acme/widgets/statuses/"+headSHA, git.CommitStateError, "error"),
	Entry("gitlab pending", models.ConnectionTypeGitlab, "", "https://gitlab.example.com/acme/cli", "/api/v4/projects/acme%2Fcli/statuses/"+headSHA, git.CommitStatePending, "pending"),
	Entry("gitlab success", models.ConnectionTypeGitlab, "", "https://gitlab.example.com/acme/cli", "/api/v4/projects/acme%2Fcli/statuses/"+headSHA, git.CommitStateSuccess, "success"),
	Entry("gitlab error", models.ConnectionTypeGitlab, "", "https://gitlab.example.com/acme/cli", "/api/v4/projects/acme%2Fcli/statuses/"+headSHA, git.CommitStateError, "failed"),
	Entry("azure success", models.ConnectionTypeAzureDevops, "/acme", "https://dev.azure.com/acme/Platform/_git/rules", "/acme/Platform/_apis/git/repositories/rules/commits/"+headSHA+"/statuses", git.CommitStateSuccess, "succeeded"),
	Entry("azure error", models.ConnectionTypeAzureDevops, "/acme", "https://dev.azure.com/acme/Platform/_git/rules", "/acme/Platform/_apis/git/repositories/rules/commits/"+headSHA+"/statuses", git.CommitStateError, "error"),
)

var _ = DescribeTable("refuses an incomplete commit status or comment before calling the API",
	func(ctx SpecContext, call func(context.Context, git.Provider, git.RepoURL) error, message string) {
		api := newFakeAPI(nil)
		provider := providerClient(models.ConnectionTypeGithub, api.Server.URL, nil).Provider
		Expect(call(ctx, provider, repoURL("https://github.com/acme/widgets", ""))).To(MatchError(ContainSubstring(message)))
		Expect(api.Calls()).To(BeEmpty())
	},
	Entry("no sha", func(ctx context.Context, p git.Provider, r git.RepoURL) error {
		return p.SetCommitStatus(ctx, r, "", applyStatus)
	}, "commit sha"),
	Entry("unknown state", func(ctx context.Context, p git.Provider, r git.RepoURL) error {
		return p.SetCommitStatus(ctx, r, headSHA, git.CommitStatus{State: "done", Context: "ci"})
	}, `unknown commit state "done"`),
	Entry("no context", func(ctx context.Context, p git.Provider, r git.RepoURL) error {
		return p.SetCommitStatus(ctx, r, headSHA, git.CommitStatus{State: git.CommitStateSuccess})
	}, "context"),
	Entry("no comment body", func(ctx context.Context, p git.Provider, r git.RepoURL) error {
		_, err := p.CommentPullRequest(ctx, r, 5, " ")
		return err
	}, "comment body"),
	Entry("no pull request number", func(ctx context.Context, p git.Provider, r git.RepoURL) error {
		_, err := p.CommentPullRequest(ctx, r, 0, "x")
		return err
	}, "pull request number"),
)

var _ = Describe("plain git provider", func() {
	It("refuses commit statuses and pull request comments", func(ctx SpecContext) {
		fixture := newBareRepo("rules")
		provider := providerClient(models.ConnectionTypeGit, fixture.FileURL(), nil).Provider
		repo := repoURL(fixture.FileURL(), "")
		Expect(provider.SetCommitStatus(ctx, repo, headSHA, applyStatus)).To(MatchError(ContainSubstring("commit statuses is not supported")))
		_, err := provider.CommentPullRequest(ctx, repo, 1, "x")
		Expect(err).To(MatchError(ContainSubstring("pull request comments is not supported")))
		_, err = provider.ListPullRequestActivity(ctx, repo, 1)
		Expect(err).To(MatchError(git.ErrPullRequestsUnsupported))
	})
})

var _ = Describe("pull request detail activity", func() {
	It("reads GitHub conversation, review comments, and issue events", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /repos/acme/widgets/issues/5/comments": reply(`[{"body":"Conversation","html_url":"https://github.com/acme/widgets/pull/5#issuecomment-1","created_at":"2026-10-18T10:00:00Z","user":{"login":"Grace"}}]`),
			"GET /repos/acme/widgets/pulls/5/comments":  reply(`[{"body":"Review","html_url":"https://github.com/acme/widgets/pull/5#discussion_r2","created_at":"2026-10-18T11:00:00Z","user":{"login":"Lin"}}]`),
			"GET /repos/acme/widgets/issues/5/events":   reply(`[{"event":"review_requested","created_at":"2026-10-18T12:00:00Z","actor":{"login":"Ada"}}]`),
		})
		provider := providerClient(models.ConnectionTypeGithub, api.Server.URL, nil).Provider
		activity, err := provider.ListPullRequestActivity(ctx, repoURL("https://github.com/acme/widgets", ""), 5)
		Expect(err).ToNot(HaveOccurred())
		Expect(activity).To(HaveLen(3))
		Expect([]git.PullRequestActivityKind{activity[0].Kind, activity[1].Kind, activity[2].Kind}).To(Equal([]git.PullRequestActivityKind{git.PullRequestComment, git.PullRequestComment, git.PullRequestHistory}))
		Expect([]string{activity[0].Body, activity[1].Body, activity[2].Body}).To(Equal([]string{"Conversation", "Review", "review requested"}))
	})

	It("reads GitLab comments, discussion replies and system history", func(ctx SpecContext) {
		path := "/api/v4/projects/acme%2Fcli/merge_requests/3"
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET " + path:                  reply(`{"iid":3,"web_url":"https://gl/acme/cli/-/merge_requests/3","state":"opened","description":"Why","author":{"name":"Ada"},"source_branch":"apply","target_branch":"main","source_project_id":7,"target_project_id":7}`),
			"GET " + path + "/discussions": reply(`[{"id":"root","individual_note":true,"notes":[{"id":10,"body":"Please test","system":false,"created_at":"2026-10-18T10:00:00Z","author":{"name":"Grace"}}]},{"id":"review","notes":[{"id":12,"type":"DiffNote","body":"Update this line","system":false,"created_at":"2026-10-18T10:30:00Z","author":{"name":"Lin"}},{"id":13,"type":"DiscussionNote","body":"Updated","system":false,"created_at":"2026-10-18T10:45:00Z","author":{"name":"Ada"}}]},{"id":"event","individual_note":true,"notes":[{"id":11,"body":"changed the title","system":true,"created_at":"2026-10-18T11:00:00Z","author":{"name":"Ada"}}]}]`),
		})
		provider := providerClient(models.ConnectionTypeGitlab, api.Server.URL, nil).Provider
		repo := repoURL("https://gitlab.example.com/acme/cli", models.ConnectionTypeGitlab)
		pr, err := provider.GetPullRequest(ctx, repo, 3)
		Expect(err).ToNot(HaveOccurred())
		Expect([]string{pr.Author, pr.Body}).To(Equal([]string{"Ada", "Why"}))
		activity, err := provider.ListPullRequestActivity(ctx, repo, 3)
		Expect(err).ToNot(HaveOccurred())
		Expect(activity).To(HaveLen(4))
		Expect(activity[0].URL).To(Equal("https://gl/acme/cli/-/merge_requests/3#note_10"))
		Expect(activity[1].Body).To(Equal("Update this line"))
		Expect(activity[2].Body).To(Equal("Updated"))
		Expect(activity[3].Kind).To(Equal(git.PullRequestHistory))
	})

	It("reads Azure creator, body, comments and system history", func(ctx SpecContext) {
		path := "/acme/Platform/_apis/git/repositories/rules/pullrequests/9"
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET " + path:              reply(`{"pullRequestId":9,"status":"active","title":"T","description":"Why this changes","creationDate":"2026-10-18T09:00:00Z","createdBy":{"displayName":"Ada"},"sourceRefName":"refs/heads/apply","targetRefName":"refs/heads/main","repository":{"name":"rules","project":{"name":"Platform"}}}`),
			"GET " + path + "/threads": reply(`{"value":[{"id":31,"comments":[{"id":1,"content":"Please test","commentType":"text","publishedDate":"2026-10-18T10:00:00Z","author":{"displayName":"Grace"}},{"id":2,"content":"Updated source","commentType":"system","publishedDate":"2026-10-18T11:00:00Z","author":{"displayName":"Ada"}}]}]}`),
		})
		provider := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider
		repo := repoURL("https://dev.azure.com/acme/Platform/_git/rules", "")

		pr, err := provider.GetPullRequest(ctx, repo, 9)
		Expect(err).ToNot(HaveOccurred())
		Expect([]string{pr.Author, pr.Body, pr.CreatedAt.Format("2006-01-02T15:04:05Z")}).To(Equal([]string{"Ada", "Why this changes", "2026-10-18T09:00:00Z"}))
		activity, err := provider.ListPullRequestActivity(ctx, repo, 9)
		Expect(err).ToNot(HaveOccurred())
		Expect(activity).To(Equal([]git.PullRequestActivity{
			{Kind: git.PullRequestComment, Author: "Grace", Body: "Please test", CreatedAt: pr.CreatedAt.Add(1 * time.Hour), URL: pr.URL + "?discussionId=31", ID: "thread:31:comment:1", Action: "text", Mentions: []string{}},
			{Kind: git.PullRequestHistory, Author: "Ada", Body: "Updated source", CreatedAt: pr.CreatedAt.Add(2 * time.Hour), URL: pr.URL + "?discussionId=31", ID: "thread:31:comment:2", Action: "system", Mentions: []string{}},
		}))
	})
})
