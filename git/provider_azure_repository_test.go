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

const azureSourceRepositoryID = "11111111-2222-3333-4444-555555555555"

func azureForkPull(number int, status, repository string) string {
	return fmt.Sprintf(`{"pullRequestId":%d,"status":%q,"title":"Update rules",`+
		`"sourceRefName":"refs/heads/apply","targetRefName":"refs/heads/main",`+
		`"lastMergeSourceCommit":{"commitId":%q},"forkSource":{"repository":%s},`+
		`"repository":{"name":"rules","project":{"name":"Platform"}}}`, number, status, headSHA, repository)
}

func azureRepository() git.RepoURL {
	return repoURL("https://dev.azure.com/acme/Platform/_git/rules", "")
}

var _ = Describe("Azure DevOps head repository resolution", func() {
	const pullPath = "/acme/Platform/_apis/git/repositories/rules/pullrequests"
	const sourcePath = "/acme/_apis/git/repositories/" + azureSourceRepositoryID
	const shallowSource = `{"id":"` + azureSourceRepositoryID + `","name":"rules-source","project":null}`

	DescribeTable("lists shallow fork references with their actual source project",
		func(ctx SpecContext, status string, state git.PullRequestState, project string) {
			api := newFakeAPI(map[string]func(*http.Request) apiReply{
				"GET " + pullPath: reply(`{"value":[` + azureForkPull(9, status, shallowSource) + `]}`),
				"GET " + sourcePath: reply(fmt.Sprintf(`{"id":%q,"name":"rules-source","project":{"name":%q}}`,
					azureSourceRepositoryID, project)),
			})
			provider := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider

			pulls, err := provider.ListPullRequests(ctx, azureRepository(), git.ListPullRequestsOptions{Base: "main", State: state, Limit: 26})

			Expect(err).NotTo(HaveOccurred())
			Expect(pulls).To(Equal([]git.PullRequest{{Number: 9, URL: "https://dev.azure.com/acme/Platform/_git/rules/pullrequest/9",
				State: state, Head: "apply", Base: "main", Title: "Update rules", HeadSHA: headSHA, HeadRepository: project + "/rules-source"}}))
			Expect(api.Calls()).To(HaveLen(2))
			Expect(api.Calls()[0].Query).To(ContainSubstring("searchCriteria.status=" + status))
			Expect(api.Calls()[1].Query).To(Equal("api-version=7.1"))
			Expect(api.Calls()[1].Authorization).To(Equal(api.Calls()[0].Authorization))
		},
		Entry("merged in the same project", "completed", git.PullRequestMerged, "Platform"),
		Entry("open from another project", "active", git.PullRequestOpen, "Forks"),
		Entry("closed from another project", "abandoned", git.PullRequestClosed, "Forks"),
	)

	It("resolves each repository once across pages without sharing the cache between list calls", func(ctx SpecContext) {
		const pageSize = 100
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET " + pullPath: func(r *http.Request) apiReply {
				start, count := 1, pageSize
				if r.URL.Query().Get("$skip") == "100" {
					start, count = pageSize+1, 1
				}
				if r.URL.Query().Get("$top") == "1" {
					count = 1
				}
				pulls := make([]string, count)
				for i := range pulls {
					pulls[i] = azureForkPull(start+i, "completed", shallowSource)
				}
				return apiReply{Body: `{"value":[` + strings.Join(pulls, ",") + `]}`}
			},
			"GET " + sourcePath: reply(`{"name":"rules-source","project":{"name":"Forks"}}`),
		})
		provider := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider

		pulls, err := provider.ListPullRequests(ctx, azureRepository(), git.ListPullRequestsOptions{State: git.PullRequestMerged, Limit: pageSize + 1})

		Expect(err).NotTo(HaveOccurred())
		Expect(pulls).To(HaveLen(pageSize + 1))
		Expect(pulls).To(HaveEach(HaveField("HeadRepository", "Forks/rules-source")))
		Expect(pulls[pageSize].Number).To(Equal(pageSize + 1))
		Expect(api.Calls()).To(HaveLen(3))
		Expect(api.Calls()[2].Query).To(ContainSubstring("%24skip=100"))

		_, err = provider.ListPullRequests(ctx, azureRepository(), git.ListPullRequestsOptions{State: git.PullRequestMerged, Limit: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(api.Calls()).To(HaveLen(5))
		Expect(api.Calls()[4].Path).To(Equal(sourcePath))
	})

	DescribeTable("resolves incomplete repository references on detail and creation responses",
		func(ctx SpecContext, method, suffix string, fork bool) {
			pull := azureForkPull(9, "active", shallowSource)
			if !fork {
				pull = `{"pullRequestId":9,"status":"active","repository":{"id":"` + azureSourceRepositoryID + `"}}`
			}
			api := newFakeAPI(map[string]func(*http.Request) apiReply{
				method + " " + pullPath + suffix: reply(pull),
				"GET " + sourcePath:              reply(`{"name":"rules-source","project":{"name":"Forks"}}`),
			})
			provider := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider
			var result git.PullRequest
			var err error
			if method == http.MethodPost {
				result, err = provider.OpenPullRequest(ctx, azureRepository(), git.PullRequestRequest{Head: "apply", Base: "main", Title: "Update rules"})
			} else {
				result, err = provider.GetPullRequest(ctx, azureRepository(), 9)
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(result.HeadRepository).To(Equal("Forks/rules-source"))
			Expect(api.Calls()).To(HaveLen(2))
		},
		Entry("fork detail", http.MethodGet, "/9", true),
		Entry("same-repository detail", http.MethodGet, "/9", false),
		Entry("created pull request", http.MethodPost, "", false),
	)

	It("does not look up complete repository references", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET " + pullPath: reply(`{"value":[` + azureForkPull(9, "completed",
				`{"name":"rules-source","project":{"name":"Forks"}}`) + `]}`),
		})
		provider := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider
		pulls, err := provider.ListPullRequests(ctx, azureRepository(), git.ListPullRequestsOptions{State: git.PullRequestMerged})
		Expect(err).NotTo(HaveOccurred())
		Expect(pulls).To(HaveExactElements(HaveField("HeadRepository", "Forks/rules-source")))
		Expect(api.Calls()).To(HaveLen(1))
	})

	DescribeTable("reports repository resolution failures with the pull request number",
		func(ctx SpecContext, source string, lookup apiReply, message string, calls int) {
			api := newFakeAPI(map[string]func(*http.Request) apiReply{
				"GET " + pullPath:   reply(`{"value":[` + azureForkPull(9, "completed", source) + `]}`),
				"GET " + sourcePath: func(*http.Request) apiReply { return lookup },
			})
			provider := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider
			pulls, err := provider.ListPullRequests(ctx, azureRepository(), git.ListPullRequestsOptions{State: git.PullRequestMerged})
			Expect(err).To(MatchError(And(ContainSubstring("pull request 9"), ContainSubstring(message))))
			Expect(pulls).To(BeNil())
			Expect(api.Calls()).To(HaveLen(calls))
		},
		Entry("missing repository ID", `{"name":"rules-source"}`, apiReply{}, "no head repository ID", 1),
		Entry("forbidden source repository", shallowSource, apiReply{Status: http.StatusForbidden}, "HTTP 403", 2),
		Entry("deleted source repository", shallowSource, apiReply{Status: http.StatusNotFound}, "HTTP 404", 2),
		Entry("lookup still lacks its project", shallowSource, apiReply{Body: `{"name":"rules-source","project":null}`}, "no head repository name and project", 2),
		Entry("lookup still lacks its name", shallowSource, apiReply{Body: `{"project":{"name":"Forks"}}`}, "no head repository name and project", 2),
	)
})
