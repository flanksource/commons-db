// Tests reading the CI a pull request ran: the current execution of each pipeline that built its
// merge result or head, with its jobs and steps from the latest attempt, and a log's tail.
package git_test

import (
	"net/http"
	"strconv"
	"time"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func itoa(n int) string { return strconv.Itoa(n) }

const (
	mergeSHA = "2222222222222222222222222222222222222222"
	staleSHA = "3333333333333333333333333333333333333333"
)

var _ = Describe("Azure DevOps pull request executions", func() {
	pull := func(fork string) func(*http.Request) apiReply {
		return reply(`{"pullRequestId":9,"status":"active","sourceRefName":"refs/heads/tighten","targetRefName":"refs/heads/main",
			"lastMergeSourceCommit":{"commitId":"` + headSHA + `"},"lastMergeCommit":{"commitId":"` + mergeSHA + `"},` + fork +
			`"repository":{"id":"rules-id","name":"rules","project":{"name":"Platform"}}}`)
	}
	build := func(id int, definition int, name, status, result, branch, sha, queued string) string {
		return `{"id":` + itoa(id) + `,"buildNumber":"b` + itoa(id) + `","status":"` + status + `","result":"` + result + `","sourceBranch":"` + branch +
			`","sourceVersion":"` + sha + `","queueTime":"` + queued + `","definition":{"id":` + itoa(definition) + `,"name":"` + name + `"},` +
			`"_links":{"web":{"href":"https://dev.azure.com/acme/Platform/_build/results?buildId=` + itoa(id) + `"}}}`
	}
	builds := func(byBranch map[string]string) func(*http.Request) apiReply {
		return func(r *http.Request) apiReply {
			return apiReply{Body: `{"value":[` + byBranch[r.URL.Query().Get("repositoryId")+" "+r.URL.Query().Get("branchName")] + `]}`}
		}
	}
	rules := func() git.RepoURL { return repoURL("https://dev.azure.com/acme/Platform/_git/rules", "") }

	It("is the latest execution of each pipeline that built the merge result or head, with its jobs from the latest attempt", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/Platform/_apis/git/repositories/rules/pullrequests/9": pull(""),
			"GET /acme/Platform/_apis/build/builds": builds(map[string]string{
				"rules-id refs/pull/9/merge": build(30, 4, "ci", "completed", "succeeded", "refs/pull/9/merge", mergeSHA, "2026-11-02T10:00:00Z") + "," +
					build(31, 4, "ci", "completed", "failed", "refs/pull/9/merge", mergeSHA, "2026-11-02T11:00:00Z") + "," +
					build(28, 6, "docs", "completed", "succeeded", "refs/pull/9/merge", staleSHA, "2026-11-02T12:00:00Z"),
				"rules-id refs/heads/tighten": build(32, 5, "lint", "notStarted", "", "refs/heads/tighten", headSHA, "2026-11-02T11:30:00Z"),
			}),
			"GET /acme/Platform/_apis/build/builds/31/timeline": reply(`{"records":[
				{"id":"stage","type":"Stage","name":"Build","state":"completed","result":"failed","order":1},
				{"id":"job-1","parentId":"stage","identifier":"build","type":"Job","name":"build","state":"completed","result":"succeeded","order":1,"attempt":1,"log":{"id":6}},
				{"id":"job-2","parentId":"stage","identifier":"build","type":"Job","name":"build","state":"completed","result":"failed","order":1,"attempt":2,
				 "startTime":"2026-11-02T11:01:00Z","finishTime":"2026-11-02T11:05:00Z","log":{"id":7}},
				{"id":"phase","parentId":"job-2","type":"Phase","name":"steps","state":"completed","result":"failed","order":1},
				{"id":"compile","parentId":"phase","type":"Task","name":"compile","state":"completed","result":"failed","order":2,"log":{"id":8}},
				{"id":"checkout","parentId":"job-2","type":"Task","name":"checkout","state":"completed","result":"succeeded","order":1,"log":{"id":9}},
				{"id":"old-step","parentId":"job-1","type":"Task","name":"compile","state":"completed","result":"succeeded","order":2}]}`),
		})
		provider := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider
		reader, ok := provider.(git.PullRequestPipelineReader)
		Expect(ok).To(BeTrue())

		executions, err := reader.PullRequestExecutions(ctx, rules(), 9)
		Expect(err).ToNot(HaveOccurred())
		Expect(executions).To(HaveLen(2))
		ci, lint := executions[0], executions[1]
		Expect(ci.ID).To(Equal("31"), "the latest of its pipeline's executions; one of a superseded merge is left out")
		Expect(ci.PipelineName).To(Equal("ci"))
		Expect(ci.Pipeline).To(Equal("4"))
		Expect(ci.Status).To(Equal("completed"))
		Expect(ci.Result).To(Equal("failed"))
		Expect(ci.SHA).To(Equal(mergeSHA))
		Expect(ci.URL).To(Equal("https://dev.azure.com/acme/Platform/_build/results?buildId=31"))
		Expect(ci.Repository.Name).To(Equal("rules"))
		Expect(ci.Jobs).To(Equal([]git.PipelineJob{{ID: "job-2", Name: "build", Status: "completed", Result: "failed",
			StartedAt: time.Date(2026, 11, 2, 11, 1, 0, 0, time.UTC), FinishedAt: time.Date(2026, 11, 2, 11, 5, 0, 0, time.UTC),
			URL: "https://dev.azure.com/acme/Platform/_build/results?buildId=31&view=logs&j=job-2", LogID: "7",
			Steps: []git.PipelineStep{
				{Name: "checkout", Number: 1, Status: "completed", Result: "succeeded", LogID: "9"},
				{Name: "compile", Number: 2, Status: "completed", Result: "failed", LogID: "8"},
			}}}))
		Expect(lint.ID).To(Equal("32"))
		Expect(lint.Status).To(Equal("notStarted"))
		Expect(lint.Jobs).To(BeEmpty(), "an execution that hasn't started has no timeline yet")
	})

	It("reads a fork's head executions in the fork's project", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/Platform/_apis/git/repositories/rules/pullrequests/9": pull(`"forkSource":{"repository":{"id":"fork-id","name":"rules-fork","project":{"name":"Forks"}}},`),
			"GET /acme/Platform/_apis/build/builds":                          builds(map[string]string{}),
			"GET /acme/Forks/_apis/build/builds": builds(map[string]string{
				"fork-id refs/heads/tighten": build(40, 7, "fork-ci", "inProgress", "", "refs/heads/tighten", headSHA, "2026-11-02T11:30:00Z"),
			}),
			"GET /acme/Forks/_apis/build/builds/40/timeline": reply(`{"records":[]}`),
		})
		reader := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider.(git.PullRequestPipelineReader)

		executions, err := reader.PullRequestExecutions(ctx, rules(), 9)
		Expect(err).ToNot(HaveOccurred())
		Expect(executions).To(HaveLen(1))
		Expect(executions[0].Repository.Project).To(Equal("Forks"))
		Expect(executions[0].Repository.Name).To(Equal("rules-fork"))
	})

	It("resolves a fork Azure names only by ID", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/Platform/_apis/git/repositories/rules/pullrequests/9": pull(`"forkSource":{"repository":{"id":"fork-id"}},`),
			"GET /acme/_apis/git/repositories/fork-id":                       reply(`{"id":"fork-id","name":"rules-fork","project":{"name":"Forks"}}`),
			"GET /acme/Platform/_apis/build/builds":                          builds(map[string]string{}),
			"GET /acme/Forks/_apis/build/builds": builds(map[string]string{
				"fork-id refs/heads/tighten": build(40, 7, "fork-ci", "inProgress", "", "refs/heads/tighten", headSHA, "2026-11-02T11:30:00Z"),
			}),
			"GET /acme/Forks/_apis/build/builds/40/timeline": reply(`{"records":[]}`),
		})
		reader := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider.(git.PullRequestPipelineReader)

		executions, err := reader.PullRequestExecutions(ctx, rules(), 9)
		Expect(err).ToNot(HaveOccurred())
		Expect(executions).To(HaveLen(1))
		Expect([]string{executions[0].Repository.Project, executions[0].Repository.Name}).To(Equal([]string{"Forks", "rules-fork"}))
	})

	It("refuses a fork lookup that answers with another repository", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/Platform/_apis/git/repositories/rules/pullrequests/9": pull(`"forkSource":{"repository":{"id":"fork-id"}},`),
			"GET /acme/_apis/git/repositories/fork-id":                       reply(`{"id":"rules-id","name":"rules","project":{"name":"Platform"}}`),
		})
		reader := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider.(git.PullRequestPipelineReader)
		_, err := reader.PullRequestExecutions(ctx, rules(), 9)
		Expect(err).To(MatchError(ContainSubstring(`resolving head repository "fork-id" found repository "rules-id"`)))
	})

	It("reads the tail of an execution's log", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/Platform/_apis/build/builds/31/logs": reply(`{"value":[{"id":7,"lineCount":40},{"id":8,"lineCount":3}]}`),
			"GET /acme/Platform/_apis/build/builds/31/logs/8": func(r *http.Request) apiReply {
				return apiReply{Body: "line " + r.URL.Query().Get("startLine") + "\r\nsecond\r\nthird\r\n"}
			},
		})
		reader := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider.(git.PullRequestPipelineReader)

		tail, err := reader.PipelineLog(ctx, rules(), "31", "8", 2)
		Expect(err).ToNot(HaveOccurred())
		Expect(tail).To(Equal("second\nthird"))
		Expect(api.Calls()[1].Query).To(ContainSubstring("startLine=1"), "only the tail's lines are fetched")

		_, err = reader.PipelineLog(ctx, rules(), "31", "99", 2)
		Expect(err).To(MatchError(ContainSubstring("azure build 31 has no log 99")))
	})
})

var _ = Describe("GitLab merge request executions", func() {
	mr := func(source int) func(*http.Request) apiReply {
		return reply(`{"iid":5,"state":"opened","source_branch":"fast","target_branch":"main","sha":"` + headSHA +
			`","source_project_id":` + itoa(source) + `,"target_project_id":1}`)
	}
	api := func() git.RepoURL {
		return repoURL("https://gitlab.example.com/acme/api.git", models.ConnectionTypeGitlab)
	}

	It("is the merge request's latest pipeline, with its jobs in the order they were created", func(ctx SpecContext) {
		fake := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /api/v4/projects/acme%2Fapi/merge_requests/5": mr(1),
			"GET /api/v4/projects/acme%2Fapi/merge_requests/5/pipelines": reply(`[
				{"id":102,"project_id":1,"sha":"` + headSHA + `","ref":"refs/merge-requests/5/head","status":"failed","created_at":"2026-11-03T11:00:00Z","web_url":"https://gitlab.example.com/acme/api/-/pipelines/102"},
				{"id":101,"project_id":1,"sha":"` + staleSHA + `","ref":"refs/merge-requests/5/head","status":"success","created_at":"2026-11-03T10:00:00Z","web_url":"https://gitlab.example.com/acme/api/-/pipelines/101"}]`),
			"GET /api/v4/projects/1/pipelines/102/jobs": reply(`[
				{"id":7002,"name":"test","stage":"test","status":"failed","started_at":"2026-11-03T11:02:00Z","finished_at":"2026-11-03T11:04:00Z","web_url":"https://gitlab.example.com/acme/api/-/jobs/7002"},
				{"id":7001,"name":"build","stage":"build","status":"success","started_at":"2026-11-03T11:00:30Z","finished_at":"2026-11-03T11:01:30Z","web_url":"https://gitlab.example.com/acme/api/-/jobs/7001"}]`),
			"GET /api/v4/projects/acme%2Fapi/jobs/7002/trace": reply("go test ./...\n--- FAIL: TestSpeed\nFAIL\n"),
		})
		reader, ok := providerClient(models.ConnectionTypeGitlab, fake.Server.URL, nil).Provider.(git.PullRequestPipelineReader)
		Expect(ok).To(BeTrue())

		executions, err := reader.PullRequestExecutions(ctx, api(), 5)
		Expect(err).ToNot(HaveOccurred())
		Expect(executions).To(Equal([]git.PullRequestExecution{{
			PipelineName: "pipeline", Repository: api(),
			PipelineExecution: git.PipelineExecution{ID: "102", Number: "102", Branch: "refs/merge-requests/5/head", SHA: headSHA, Status: "failed",
				CreatedAt: time.Date(2026, 11, 3, 11, 0, 0, 0, time.UTC), URL: "https://gitlab.example.com/acme/api/-/pipelines/102"},
			Jobs: []git.PipelineJob{
				{ID: "7001", Name: "build", Status: "success", StartedAt: time.Date(2026, 11, 3, 11, 0, 30, 0, time.UTC),
					FinishedAt: time.Date(2026, 11, 3, 11, 1, 30, 0, time.UTC), URL: "https://gitlab.example.com/acme/api/-/jobs/7001", LogID: "7001"},
				{ID: "7002", Name: "test", Status: "failed", StartedAt: time.Date(2026, 11, 3, 11, 2, 0, 0, time.UTC),
					FinishedAt: time.Date(2026, 11, 3, 11, 4, 0, 0, time.UTC), URL: "https://gitlab.example.com/acme/api/-/jobs/7002", LogID: "7002"},
			},
		}}))

		log, err := reader.PipelineLog(ctx, executions[0].Repository, "102", "7002", 2)
		Expect(err).ToNot(HaveOccurred())
		Expect(log).To(Equal("--- FAIL: TestSpeed\nFAIL"))
	})

	It("reads a fork's pipeline through the fork's project", func(ctx SpecContext) {
		fake := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /api/v4/projects/acme%2Fapi/merge_requests/5": mr(9),
			"GET /api/v4/projects/9":                           reply(`{"id":9,"path":"api","path_with_namespace":"grace/api"}`),
			"GET /api/v4/projects/acme%2Fapi/merge_requests/5/pipelines": reply(`[
				{"id":202,"project_id":9,"sha":"` + headSHA + `","ref":"fast","status":"running","created_at":"2026-11-03T11:00:00Z"}]`),
			"GET /api/v4/projects/9/pipelines/202/jobs": reply(`[]`),
		})
		reader := providerClient(models.ConnectionTypeGitlab, fake.Server.URL, nil).Provider.(git.PullRequestPipelineReader)

		executions, err := reader.PullRequestExecutions(ctx, api(), 5)
		Expect(err).ToNot(HaveOccurred())
		Expect(executions).To(HaveLen(1))
		Expect(executions[0].Repository.Owner).To(Equal("grace"))
		Expect(executions[0].Repository.Name).To(Equal("api"))
		Expect(executions[0].Status).To(Equal("running"))
	})

	It("has none for a merge request that ran no pipeline", func(ctx SpecContext) {
		fake := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /api/v4/projects/acme%2Fapi/merge_requests/5":           mr(1),
			"GET /api/v4/projects/acme%2Fapi/merge_requests/5/pipelines": reply(`[]`),
		})
		reader := providerClient(models.ConnectionTypeGitlab, fake.Server.URL, nil).Provider.(git.PullRequestPipelineReader)
		executions, err := reader.PullRequestExecutions(ctx, api(), 5)
		Expect(err).ToNot(HaveOccurred())
		Expect(executions).To(BeEmpty())
	})
})
