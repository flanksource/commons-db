package git_test

import (
	"net/http"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

var _ = Describe("pipeline discovery", func() {
	It("lists GitHub workflows and preserves pagination and execution provenance", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /repos/acme/rules/actions/workflows": func(r *http.Request) apiReply {
				Expect(r.URL.Query().Get("per_page")).To(Equal("2"))
				return apiReply{Headers: map[string]string{"Link": `<{base}/repos/acme/rules/actions/workflows?page=2&per_page=2>; rel="next"`}, Body: `{"workflows":[{"id":17,"name":"Validate rules","path":".github/workflows/validate.yaml","html_url":"https://example.test/workflows/17","state":"active"}]}`}
			},
			"GET /repos/acme/rules/actions/workflows/17/runs": func(r *http.Request) apiReply {
				Expect(r.URL.Query().Get("branch")).To(Equal("release/uat"))
				return apiReply{Body: `{"workflow_runs":[{"id":81,"workflow_id":17,"run_number":9,"run_attempt":2,"display_title":"Validate change","status":"completed","conclusion":"success","head_branch":"release/uat","head_sha":"1111111111111111111111111111111111111111","created_at":"2026-10-20T10:00:00Z","html_url":"https://example.test/runs/81","actor":{"login":"alex"}}]}`}
			},
		})
		client := providerClient(models.ConnectionTypeGithub, api.Server.URL, nil)
		provider, err := git.PipelineAPI(client.Provider)
		Expect(err).NotTo(HaveOccurred())
		repo := repoURL("https://github.com/acme/rules", models.ConnectionTypeGithub)
		page, err := provider.ListPipelines(ctx, git.PipelineOptions{Repository: repo, Limit: 2})
		Expect(err).NotTo(HaveOccurred())
		Expect(page.Items).To(Equal([]git.Pipeline{{ID: "17", Name: "Validate rules", Path: ".github/workflows/validate.yaml", URL: "https://example.test/workflows/17", State: "active"}}))
		Expect(page.NextCursor).To(Equal("2"))
		runs, err := provider.ListPipelineExecutions(ctx, git.PipelineOptions{Repository: repo, Pipeline: "17", Branch: "refs/heads/release/uat", Limit: 2})
		Expect(err).NotTo(HaveOccurred())
		Expect(runs.Items).To(HaveLen(1))
		Expect(runs.Items[0]).To(MatchFields(IgnoreExtras, Fields{"ID": Equal("81"), "Pipeline": Equal("17"), "Number": Equal("9"), "Attempt": Equal(2), "Branch": Equal("refs/heads/release/uat"), "SHA": Equal("1111111111111111111111111111111111111111"), "Status": Equal("completed"), "Result": Equal("success"), "Author": Equal("alex")}))
	})

	It("scopes Azure definitions and builds to the repository and project", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /Project/_apis/git/repositories/rules": reply(`{"id":"repo-id"}`),
			"GET /Project/_apis/build/definitions": func(r *http.Request) apiReply {
				Expect(r.URL.Query().Get("repositoryId")).To(Equal("repo-id"))
				Expect(r.URL.Query().Get("repositoryType")).To(Equal("TfsGit"))
				return apiReply{Headers: map[string]string{"x-ms-continuationtoken": "next"}, Body: `{"value":[{"id":23,"name":"Rules CI","path":"\\Rules","_links":{"web":{"href":"https://example.test/definitions/23"}}}]}`}
			},
			"GET /Project/_apis/build/builds": func(r *http.Request) apiReply {
				Expect(r.URL.Query().Get("definitions")).To(Equal("23"))
				Expect(r.URL.Query().Get("branchName")).To(Equal("refs/heads/main"))
				return apiReply{Body: `{"value":[{"id":91,"buildNumber":"20261020.1","status":"completed","result":"succeeded","sourceBranch":"refs/heads/main","sourceVersion":"2222222222222222222222222222222222222222","queueTime":"2026-10-20T10:00:00Z","definition":{"id":23},"requestedFor":{"displayName":"Alex"},"_links":{"web":{"href":"https://example.test/builds/91"}}}]}`}
			},
		})
		client := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL, map[string]string{"project": "Project"})
		provider, err := git.PipelineAPI(client.Provider)
		Expect(err).NotTo(HaveOccurred())
		repo := repoURL("https://dev.azure.com/acme/Project/_git/rules", models.ConnectionTypeAzureDevops)
		page, err := provider.ListPipelines(ctx, git.PipelineOptions{Repository: repo})
		Expect(err).NotTo(HaveOccurred())
		Expect(page.Items).To(HaveLen(1))
		Expect(page.Items[0].ID).To(Equal("23"))
		Expect(page.NextCursor).To(Equal("next"))
		runs, err := provider.ListPipelineExecutions(ctx, git.PipelineOptions{Repository: repo, Pipeline: "23", Branch: "refs/heads/main"})
		Expect(err).NotTo(HaveOccurred())
		Expect(runs.Items).To(HaveLen(1))
		Expect(runs.Items[0]).To(MatchFields(IgnoreExtras, Fields{"ID": Equal("91"), "Pipeline": Equal("23"), "Number": Equal("20261020.1"), "SHA": Equal("2222222222222222222222222222222222222222"), "Result": Equal("succeeded")}))
	})

	It("reports unsupported providers and rejects invalid pagination", func(ctx SpecContext) {
		client := providerClient(models.ConnectionTypeGitlab, "https://example.test", nil)
		_, err := git.PipelineAPI(client.Provider)
		Expect(err).To(MatchError(git.ErrPipelinesUnsupported))
		github := providerClient(models.ConnectionTypeGithub, "https://example.test", nil)
		provider, err := git.PipelineAPI(github.Provider)
		Expect(err).NotTo(HaveOccurred())
		_, err = provider.ListPipelines(ctx, git.PipelineOptions{Limit: -1})
		Expect(err).To(HaveOccurred())
		_, err = provider.ListPipelineExecutions(ctx, git.PipelineOptions{})
		Expect(err).To(HaveOccurred())
	})
})
