package git_test

import (
	"net/http"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("GitHub provider", func() {
	It("pages repositories by the Link header", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /orgs/acme/repos": func(r *http.Request) apiReply {
				if r.URL.Query().Get("page") == "2" {
					return apiReply{Body: `[{"name":"b","full_name":"acme/b","clone_url":"https://github.com/acme/b.git","default_branch":"dev","private":true}]`}
				}
				return apiReply{Headers: map[string]string{"Link": `<{base}/orgs/acme/repos?per_page=100&page=2>; rel="next"`},
					Body: `[{"name":"a","full_name":"acme/a","clone_url":"https://github.com/acme/a.git","default_branch":"main"}]`}
			},
		})
		client := providerClient(models.ConnectionTypeGithub, api.Server.URL, map[string]string{"organization": "acme"})
		repos, err := client.Provider.ListRepositories(ctx, git.ListRepositoriesOptions{})
		Expect(err).ToNot(HaveOccurred())
		Expect(repos).To(Equal([]git.RepositoryInfo{
			{Name: "a", FullName: "acme/a", Namespace: "acme", CloneURL: "https://github.com/acme/a.git", DefaultBranch: "main"},
			{Name: "b", FullName: "acme/b", Namespace: "acme", CloneURL: "https://github.com/acme/b.git", DefaultBranch: "dev", Private: true},
		}))
		calls := api.Calls()
		Expect(calls).To(HaveLen(2))
		Expect(calls[0].Query).To(Equal("per_page=100"))
		Expect(calls[0].Authorization).To(Equal("Bearer test-token"))
	})

	It("opens a pull request with labels, assignees and reviewers, and maps merged state", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"POST /repos/acme/widgets/pulls":                       reply(`{"number":5,"html_url":"https://github.com/acme/widgets/pull/5","state":"open","title":"T","head":{"ref":"apply"},"base":{"ref":"main"}}`),
			"POST /repos/acme/widgets/issues/5/labels":             reply(`[]`),
			"POST /repos/acme/widgets/issues/5/assignees":          reply(`{}`),
			"POST /repos/acme/widgets/pulls/5/requested_reviewers": reply(`{}`),
			"GET /repos/acme/widgets/pulls/5":                      reply(`{"number":5,"html_url":"u","state":"closed","merged_at":"2026-01-01T00:00:00Z","title":"T","head":{"ref":"apply"},"base":{"ref":"main"}}`),
			"PATCH /repos/acme/widgets/pulls/5":                    reply(`{}`),
		})
		provider := providerClient(models.ConnectionTypeGithub, api.Server.URL, nil).Provider
		repo := repoURL("https://github.com/acme/widgets.git", "")
		pr, err := provider.OpenPullRequest(ctx, repo, git.PullRequestRequest{Title: "T", Body: "B", Head: "apply", Base: "main",
			Labels: []string{"oipa"}, Assignees: []string{"alice"}, Reviewers: []string{"bob"}})
		Expect(err).ToNot(HaveOccurred())
		Expect(pr).To(Equal(git.PullRequest{Number: 5, URL: "https://github.com/acme/widgets/pull/5", State: git.PullRequestOpen,
			Head: "apply", Base: "main", Title: "T"}))
		got, err := provider.GetPullRequest(ctx, repo, 5)
		Expect(err).ToNot(HaveOccurred())
		Expect(got.State).To(Equal(git.PullRequestMerged))
		Expect(provider.ClosePullRequest(ctx, repo, 5)).To(Succeed())

		calls := api.Calls()
		Expect(calls[0].Body).To(Equal(map[string]any{"title": "T", "body": "B", "head": "apply", "base": "main", "draft": false}))
		Expect(calls[1].Body).To(Equal(map[string]any{"labels": []any{"oipa"}}))
		Expect(calls[2].Body).To(Equal(map[string]any{"assignees": []any{"alice"}}))
		Expect(calls[3].Body).To(Equal(map[string]any{"reviewers": []any{"bob"}}))
		Expect(calls[5].Body).To(Equal(map[string]any{"state": "closed"}))
	})

	It("refuses auto-merge before creating anything", func(ctx SpecContext) {
		api := newFakeAPI(nil)
		provider := providerClient(models.ConnectionTypeGithub, api.Server.URL, nil).Provider
		_, err := provider.OpenPullRequest(ctx, repoURL("https://github.com/acme/widgets", ""), git.PullRequestRequest{
			Title: "T", Head: "a", Base: "main", AutoMerge: true})
		Expect(err).To(MatchError(ContainSubstring("auto-merge")))
		Expect(api.Calls()).To(BeEmpty())
	})
})

var _ = Describe("GitLab provider", func() {
	It("pages group projects by X-Next-Page", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /api/v4/groups/acme%2Fplatform/projects": func(r *http.Request) apiReply {
				if r.URL.Query().Get("page") == "2" {
					return apiReply{Body: `[{"path":"b","path_with_namespace":"acme/platform/b","http_url_to_repo":"https://gl/acme/platform/b.git","default_branch":"main","namespace":{"full_path":"acme/platform"}}]`}
				}
				return apiReply{Headers: map[string]string{"X-Next-Page": "2"},
					Body: `[{"path":"a","path_with_namespace":"acme/platform/a","http_url_to_repo":"https://gl/acme/platform/a.git","default_branch":"main","visibility":"private","archived":true,"namespace":{"full_path":"acme/platform"}}]`}
			},
		})
		client := providerClient(models.ConnectionTypeGitlab, api.Server.URL, map[string]string{"group": "acme/platform"})
		repos, err := client.Provider.ListRepositories(ctx, git.ListRepositoriesOptions{})
		Expect(err).ToNot(HaveOccurred())
		Expect(repos).To(Equal([]git.RepositoryInfo{
			{Name: "a", FullName: "acme/platform/a", Namespace: "acme/platform", CloneURL: "https://gl/acme/platform/a.git", DefaultBranch: "main", Private: true, Archived: true},
			{Name: "b", FullName: "acme/platform/b", Namespace: "acme/platform", CloneURL: "https://gl/acme/platform/b.git", DefaultBranch: "main"},
		}))
		Expect(api.Calls()[0].Query).To(Equal("include_subgroups=true&per_page=100"))
		Expect(api.Calls()[1].Query).To(Equal("include_subgroups=true&page=2&per_page=100"))
	})

	It("opens a merge request with looked-up reviewers and auto-merge", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /api/v4/users": func(r *http.Request) apiReply {
				return map[string]apiReply{"alice": {Body: `[{"id":11}]`}, "bob": {Body: `[{"id":12}]`}}[r.URL.Query().Get("username")]
			},
			"POST /api/v4/projects/acme%2Fplatform%2Fcli/merge_requests":        reply(`{"iid":3,"web_url":"https://gl/mr/3","state":"opened","title":"T","source_branch":"apply","target_branch":"main"}`),
			"PUT /api/v4/projects/acme%2Fplatform%2Fcli/merge_requests/3/merge": reply(`{}`),
			"GET /api/v4/projects/acme%2Fplatform%2Fcli/merge_requests/3":       reply(`{"iid":3,"web_url":"u","state":"closed","title":"T","source_branch":"apply","target_branch":"main"}`),
			"PUT /api/v4/projects/acme%2Fplatform%2Fcli/merge_requests/3":       reply(`{}`),
		})
		provider := providerClient(models.ConnectionTypeGitlab, api.Server.URL, nil).Provider
		repo := repoURL("https://gitlab.example.com/acme/platform/cli.git", models.ConnectionTypeGitlab)
		pr, err := provider.OpenPullRequest(ctx, repo, git.PullRequestRequest{Title: "T", Body: "B", Head: "apply", Base: "main",
			Labels: []string{"x", "y"}, Assignees: []string{"alice"}, Reviewers: []string{"bob"}, AutoMerge: true})
		Expect(err).ToNot(HaveOccurred())
		Expect(pr).To(Equal(git.PullRequest{Number: 3, URL: "https://gl/mr/3", State: git.PullRequestOpen, Head: "apply", Base: "main", Title: "T"}))
		got, err := provider.GetPullRequest(ctx, repo, 3)
		Expect(err).ToNot(HaveOccurred())
		Expect(got.State).To(Equal(git.PullRequestClosed))
		Expect(provider.ClosePullRequest(ctx, repo, 3)).To(Succeed())

		calls := api.Calls()
		Expect(calls[0].Authorization).To(Equal("Bearer test-token"))
		Expect(calls[2].Body).To(Equal(map[string]any{"source_branch": "apply", "target_branch": "main", "title": "T",
			"description": "B", "labels": "x,y", "assignee_ids": []any{11.0}, "reviewer_ids": []any{12.0}}))
		Expect(calls[3].Body).To(Equal(map[string]any{"merge_when_pipeline_succeeds": true}))
		Expect(calls[5].Body).To(Equal(map[string]any{"state_event": "close"}))
	})
})

var _ = Describe("Azure DevOps provider", func() {
	It("lists project repositories with api-version and Basic PAT auth", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/Platform/_apis/git/repositories": reply(`{"value":[{"name":"rules","remoteUrl":"https://acme@dev.azure.com/acme/Platform/_git/rules","sshUrl":"git@ssh.dev.azure.com:v3/acme/Platform/rules","webUrl":"https://dev.azure.com/acme/Platform/_git/rules","defaultBranch":"refs/heads/main","isDisabled":true,"project":{"name":"Platform"}}]}`),
		})
		client := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", map[string]string{"project": "Platform"})
		repos, err := client.Provider.ListRepositories(ctx, git.ListRepositoriesOptions{})
		Expect(err).ToNot(HaveOccurred())
		Expect(repos).To(Equal([]git.RepositoryInfo{{Name: "rules", FullName: "Platform/rules", Namespace: "Platform",
			CloneURL: "https://acme@dev.azure.com/acme/Platform/_git/rules", SSHURL: "git@ssh.dev.azure.com:v3/acme/Platform/rules",
			WebURL: "https://dev.azure.com/acme/Platform/_git/rules", DefaultBranch: "main", Archived: true}}))
		Expect(api.Calls()[0].Query).To(Equal("api-version=7.1"))
		Expect(api.Calls()[0].Authorization).To(Equal("Basic OnRlc3QtdG9rZW4="))
	})

	It("opens with auto-complete, maps state and abandons as close", func(ctx SpecContext) {
		prPath := "/acme/Platform/_apis/git/repositories/rules/pullrequests"
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"POST " + prPath:         reply(`{"pullRequestId":9,"status":"active","title":"T","sourceRefName":"refs/heads/apply","targetRefName":"refs/heads/main","createdBy":{"id":"u-1"},"repository":{"webUrl":"https://dev.azure.com/acme/Platform/_git/rules"}}`),
			"PATCH " + prPath + "/9": reply(`{}`),
			"GET " + prPath + "/9":   reply(`{"pullRequestId":9,"status":"completed","title":"T","sourceRefName":"refs/heads/apply","targetRefName":"refs/heads/main","repository":{"webUrl":"https://dev.azure.com/acme/Platform/_git/rules"}}`),
		})
		provider := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider
		repo := repoURL("https://dev.azure.com/acme/Platform/_git/rules", "")
		pr, err := provider.OpenPullRequest(ctx, repo, git.PullRequestRequest{Title: "T", Body: "B", Head: "apply", Base: "main",
			Labels: []string{"oipa"}, Draft: true, AutoMerge: true})
		Expect(err).ToNot(HaveOccurred())
		Expect(pr).To(Equal(git.PullRequest{Number: 9, URL: "https://dev.azure.com/acme/Platform/_git/rules/pullrequest/9",
			State: git.PullRequestOpen, Head: "apply", Base: "main", Title: "T"}))
		got, err := provider.GetPullRequest(ctx, repo, 9)
		Expect(err).ToNot(HaveOccurred())
		Expect(got.State).To(Equal(git.PullRequestMerged))
		Expect(provider.ClosePullRequest(ctx, repo, 9)).To(Succeed())

		calls := api.Calls()
		Expect(calls[0].Body).To(Equal(map[string]any{"sourceRefName": "refs/heads/apply", "targetRefName": "refs/heads/main",
			"title": "T", "description": "B", "isDraft": true, "labels": []any{map[string]any{"name": "oipa"}}}))
		Expect(calls[1].Body).To(Equal(map[string]any{"autoCompleteSetBy": map[string]any{"id": "u-1"}}))
		Expect(calls[3].Body).To(Equal(map[string]any{"status": "abandoned"}))
	})

	It("refuses reviewers explicitly", func(ctx SpecContext) {
		api := newFakeAPI(nil)
		provider := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider
		_, err := provider.OpenPullRequest(ctx, repoURL("https://dev.azure.com/acme/Platform/_git/rules", ""),
			git.PullRequestRequest{Title: "T", Head: "a", Base: "main", Reviewers: []string{"bob"}})
		Expect(err).To(MatchError(ContainSubstring("reviewers")))
		Expect(api.Calls()).To(BeEmpty())
	})
})

var _ = Describe("Client.Repository", func() {
	It("refuses a repository outside the connection's organization", func() {
		client := providerClient(models.ConnectionTypeAzureDevops, "https://dev.azure.com/acme", nil)
		_, err := client.Repository("https://dev.azure.com/other/Platform/_git/rules")
		Expect(err).To(MatchError(ContainSubstring("organization")))
		repo, err := client.Repository("https://dev.azure.com/acme/Platform/_git/rules")
		Expect(err).ToNot(HaveOccurred())
		Expect(repo.URL.Name).To(Equal("rules"))
	})

	It("refuses a GitHub repository on another host", func() {
		client := providerClient(models.ConnectionTypeGithub, "", nil)
		_, err := client.Repository("https://gitlab.com/acme/widgets")
		Expect(err).To(MatchError(ContainSubstring("host")))
		_, err = client.Repository("https://github.com/acme/widgets")
		Expect(err).ToNot(HaveOccurred())
	})

	It("refuses an Azure DevOps repository on a host that is not Azure DevOps", func() {
		client := providerClient(models.ConnectionTypeAzureDevops, "https://dev.azure.com/acme", nil)
		_, err := client.Repository("https://evil.example.com/acme/Platform/_git/rules")
		Expect(err).To(MatchError(ContainSubstring(`host "evil.example.com"`)))
		_, err = client.Repository("https://acme.visualstudio.com/Platform/_git/rules")
		Expect(err).ToNot(HaveOccurred())

		server := providerClient(models.ConnectionTypeAzureDevops, "https://tfs.example.com/tfs/acme", nil)
		_, err = server.Repository("https://other.example.com/tfs/acme/Platform/_git/rules")
		Expect(err).To(MatchError(ContainSubstring(`host "other.example.com"`)))
		_, err = server.Repository("https://tfs.example.com/tfs/acme/Platform/_git/rules")
		Expect(err).ToNot(HaveOccurred())
	})

	DescribeTable("refuses an http repository on an https connection",
		func(kind, connectionURL, repoURL string) {
			_, err := providerClient(kind, connectionURL, nil).Repository(repoURL)
			Expect(err).To(MatchError(ContainSubstring(`scheme "http"`)))
		},
		Entry("github", models.ConnectionTypeGithub, "", "http://github.com/acme/widgets"),
		Entry("gitlab", models.ConnectionTypeGitlab, "https://gitlab.example.com", "http://gitlab.example.com/acme/widgets"),
		Entry("azure", models.ConnectionTypeAzureDevops, "https://dev.azure.com/acme", "http://dev.azure.com/acme/Platform/_git/rules"),
		Entry("git", models.ConnectionTypeGit, "https://git.example.com/acme/rules.git", "http://git.example.com/acme/other.git"),
	)

	It("verifies a plain git connection by listing its remote", func(ctx SpecContext) {
		fixture := newBareRepo("rules")
		client := providerClient(models.ConnectionTypeGit, fixture.FileURL(), nil)
		Expect(client.Provider.Verify(ctx)).To(Succeed())
		repos, err := client.Provider.ListRepositories(ctx, git.ListRepositoriesOptions{})
		Expect(err).ToNot(HaveOccurred())
		Expect(repos).To(Equal([]git.RepositoryInfo{{Name: "rules", FullName: fixture.Root[1:] + "/rules", Namespace: fixture.Root[1:],
			CloneURL: fixture.FileURL()}}))
		_, err = client.Provider.OpenPullRequest(ctx, repoURL(fixture.FileURL(), ""), git.PullRequestRequest{})
		Expect(err).To(MatchError(git.ErrPullRequestsUnsupported))
	})
})
