package git_test

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func githubPulls(from, count int) string {
	pulls := make([]string, 0, count)
	for number := from; number < from+count; number++ {
		pulls = append(pulls, fmt.Sprintf(`{"number":%d,"html_url":"u","state":"open","head":{"ref":"apply"},"base":{"ref":"main"}}`, number))
	}
	return "[" + strings.Join(pulls, ",") + "]"
}

var _ = Describe("GitHub ListPullRequests", func() {
	pullsPath := "GET /repos/acme/widgets/pulls"
	repo := func() git.RepoURL { return repoURL("https://github.com/acme/widgets", "") }

	It("filters open pull requests by base and owner-qualified head, keeping the fork head repository", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			pullsPath: reply(`[{"number":5,"html_url":"u","state":"open","title":"T","head":{"ref":"apply","sha":"` + headSHA + `","repo":{"full_name":"fork/widgets"}},"base":{"ref":"release/1"}}]`),
		})
		provider := providerClient(models.ConnectionTypeGithub, api.Server.URL, nil).Provider
		pulls, err := provider.ListPullRequests(ctx, repo(), git.ListPullRequestsOptions{Base: "release/1", Head: "apply"})
		Expect(err).ToNot(HaveOccurred())
		Expect(pulls).To(Equal([]git.PullRequest{{Number: 5, URL: "u", State: git.PullRequestOpen, Head: "apply", Base: "release/1",
			Title: "T", HeadSHA: headSHA, HeadRepository: "fork/widgets"}}))
		Expect(api.Calls()[0].Query).To(Equal("base=release%2F1&head=acme%3Aapply&page=1&per_page=100&state=open"))
	})

	It("follows pages and stops at Limit", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			pullsPath: func(r *http.Request) apiReply {
				if r.URL.Query().Get("page") == "2" {
					return apiReply{Body: githubPulls(101, 100)}
				}
				return apiReply{Body: githubPulls(1, 100)}
			},
		})
		provider := providerClient(models.ConnectionTypeGithub, api.Server.URL, nil).Provider
		pulls, err := provider.ListPullRequests(ctx, repo(), git.ListPullRequestsOptions{Limit: 150})
		Expect(err).ToNot(HaveOccurred())
		Expect(pulls).To(HaveLen(150))
		Expect(pulls[149].Number).To(Equal(150))
		Expect(api.Calls()).To(HaveLen(2))
	})

	DescribeTable("lists closed pull requests by GitHub's closed state, split by merged_at",
		func(ctx SpecContext, state git.PullRequestState, want int) {
			api := newFakeAPI(map[string]func(*http.Request) apiReply{
				pullsPath: reply(`[{"number":1,"state":"closed","merged_at":"2026-01-01T00:00:00Z"},{"number":2,"state":"closed"}]`),
			})
			provider := providerClient(models.ConnectionTypeGithub, api.Server.URL, nil).Provider
			pulls, err := provider.ListPullRequests(ctx, repo(), git.ListPullRequestsOptions{State: state})
			Expect(err).ToNot(HaveOccurred())
			Expect(pulls).To(HaveExactElements(HaveField("Number", want)))
			Expect(api.Calls()[0].Query).To(ContainSubstring("state=closed"))
		},
		Entry("merged", git.PullRequestMerged, 1),
		Entry("closed", git.PullRequestClosed, 2),
	)

	It("refuses an unknown state before calling the API", func(ctx SpecContext) {
		api := newFakeAPI(nil)
		provider := providerClient(models.ConnectionTypeGithub, api.Server.URL, nil).Provider
		_, err := provider.ListPullRequests(ctx, repo(), git.ListPullRequestsOptions{State: "draft"})
		Expect(err).To(MatchError(ContainSubstring(`unknown pull request state "draft"`)))
		Expect(api.Calls()).To(BeEmpty())
	})
})

var _ = Describe("GitLab ListPullRequests", func() {
	It("filters by target and source branch and resolves a fork source project", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /api/v4/projects/acme%2Fcli/merge_requests": reply(`[{"iid":3,"web_url":"u","state":"merged","title":"T","source_branch":"apply","target_branch":"release/1","sha":"` + headSHA + `","source_project_id":42,"target_project_id":7}]`),
			"GET /api/v4/projects/42":                        reply(`{"path":"cli","path_with_namespace":"fork/cli"}`),
		})
		provider := providerClient(models.ConnectionTypeGitlab, api.Server.URL, nil).Provider
		pulls, err := provider.ListPullRequests(ctx, repoURL("https://gitlab.example.com/acme/cli.git", models.ConnectionTypeGitlab),
			git.ListPullRequestsOptions{Base: "release/1", Head: "apply", State: git.PullRequestMerged})
		Expect(err).ToNot(HaveOccurred())
		Expect(pulls).To(Equal([]git.PullRequest{{Number: 3, URL: "u", State: git.PullRequestMerged, Head: "apply", Base: "release/1",
			Title: "T", HeadSHA: headSHA, HeadRepository: "fork/cli"}}))
		Expect(api.Calls()[0].Query).To(Equal("page=1&per_page=100&source_branch=apply&state=merged&target_branch=release%2F1"))
	})
})

var _ = Describe("Azure DevOps ListPullRequests", func() {
	It("filters by search criteria refs and pages with $top and $skip", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/Platform/_apis/git/repositories/rules/pullrequests": reply(`{"value":[{"pullRequestId":9,"status":"active","title":"T",` +
				`"sourceRefName":"refs/heads/apply","targetRefName":"refs/heads/release/1","lastMergeSourceCommit":{"commitId":"` + headSHA + `"},` +
				`"forkSource":{"repository":{"name":"rules-fork","project":{"name":"Forks"}}},` +
				`"repository":{"name":"rules","project":{"name":"Platform"},"webUrl":"https://dev.azure.com/acme/Platform/_git/rules"}}]}`),
		})
		provider := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider
		pulls, err := provider.ListPullRequests(ctx, repoURL("https://dev.azure.com/acme/Platform/_git/rules", ""),
			git.ListPullRequestsOptions{Base: "release/1", Head: "apply", Limit: 10})
		Expect(err).ToNot(HaveOccurred())
		Expect(pulls).To(Equal([]git.PullRequest{{Number: 9, URL: "https://dev.azure.com/acme/Platform/_git/rules/pullrequest/9",
			State: git.PullRequestOpen, Head: "apply", Base: "release/1", Title: "T", HeadSHA: headSHA, HeadRepository: "Forks/rules-fork"}}))
		Expect(api.Calls()[0].Query).To(Equal("api-version=7.1&%24skip=0&%24top=10&searchCriteria.sourceRefName=refs%2Fheads%2Fapply" +
			"&searchCriteria.status=active&searchCriteria.targetRefName=refs%2Fheads%2Frelease%2F1"))
	})

	It("links each pull request to its web page although a list leaves the repository's webUrl out", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/Platform/_apis/git/repositories/rules/pullrequests": reply(`{"value":[{"pullRequestId":9,"status":"active","title":"T",` +
				`"sourceRefName":"refs/heads/apply","targetRefName":"refs/heads/release/1","lastMergeSourceCommit":{"commitId":"` + headSHA + `"},` +
				`"repository":{"name":"rules","project":{"name":"Platform"}}}]}`),
		})
		provider := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider
		pulls, err := provider.ListPullRequests(ctx, repoURL("https://dev.azure.com/acme/Platform/_git/rules", ""), git.ListPullRequestsOptions{})
		Expect(err).ToNot(HaveOccurred())
		Expect(pulls).To(HaveLen(1))
		Expect(pulls[0].URL).To(Equal("https://dev.azure.com/acme/Platform/_git/rules/pullrequest/9"))
	})
})

var _ = Describe("plain git ListPullRequests", func() {
	It("returns ErrPullRequestsUnsupported", func(ctx SpecContext) {
		fixture := newBareRepo("rules")
		provider := providerClient(models.ConnectionTypeGit, fixture.FileURL(), nil).Provider
		_, err := provider.ListPullRequests(ctx, repoURL(fixture.FileURL(), ""), git.ListPullRequestsOptions{})
		Expect(err).To(MatchError(git.ErrPullRequestsUnsupported))
	})
})
