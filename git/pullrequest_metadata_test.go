package git_test

import (
	"net/http"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("pull request listing metadata", func() {
	It("combines GitHub commit statuses and check runs while preserving unknown mergeability", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /repos/acme/rules/pulls/9":                 reply(`{"number":9,"state":"open","user":{"login":"ada","avatar_url":"https://example.com/ada.png"},"mergeable":null,"mergeable_state":"unknown","head":{"ref":"feature/a","sha":"head","repo":{"full_name":"acme/rules"}},"base":{"ref":"main","sha":"base"}}`),
			"GET /repos/acme/rules/commits/head/status":     reply(`{"statuses":[{"context":"lint","state":"success","target_url":"https://example.com/lint"}]}`),
			"GET /repos/acme/rules/commits/head/check-runs": reply(`{"check_runs":[{"name":"Build","status":"in_progress","conclusion":null,"html_url":"https://example.com/build"}]}`),
		})
		metadata, err := git.ReadPullRequestMetadata(ctx, providerClient(models.ConnectionTypeGithub, api.Server.URL, nil).Provider,
			repoURL("https://github.com/acme/rules", ""), git.PullRequestMetadataOptions{Number: 9})
		Expect(err).NotTo(HaveOccurred())
		Expect(metadata.Mergeability).To(Equal("checking"))
		Expect(metadata.Checks).To(Equal([]git.PullRequestCheck{{Name: "lint", State: "success", URL: "https://example.com/lint"}, {Name: "Build", State: "pending", URL: "https://example.com/build"}}))
	})

	It("reads GitLab merge blockers and preserves failed upstream checks", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /api/v4/projects/acme%2Frules/merge_requests/9":                 reply(`{"iid":9,"state":"opened","author":{"name":"Ada","avatar_url":"https://example.com/ada.png"},"source_branch":"feature/a","target_branch":"main","sha":"head","source_project_id":1,"target_project_id":1,"detailed_merge_status":"not_approved"}`),
			"GET /api/v4/projects/acme%2Frules/repository/commits/head/statuses": reply(`[{"name":"Build","status":"failed","target_url":"https://example.com/build"}]`),
		})
		metadata, err := git.ReadPullRequestMetadata(ctx, providerClient(models.ConnectionTypeGitlab, api.Server.URL, nil).Provider,
			repoURL("https://gitlab.com/acme/rules", ""), git.PullRequestMetadataOptions{Number: 9, BaseSHA: "base"})
		Expect(err).NotTo(HaveOccurred())
		Expect(metadata.Mergeability).To(Equal("blocked"))
		Expect(metadata.MergeReason).To(Equal("not_approved"))
		Expect(metadata.AuthorAvatarURL).To(Equal("https://example.com/ada.png"))
		Expect(metadata.Checks).To(Equal([]git.PullRequestCheck{{Name: "Build", State: "failure", URL: "https://example.com/build"}}))
	})

	It("retains branch and revision links when the upstream checks cannot be read", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /repos/acme/rules/pulls/9": reply(`{"number":9,"state":"open","head":{"ref":"feature/a","sha":"head","repo":{"full_name":"acme/rules"}},"base":{"ref":"main","sha":"base"}}`),
			"GET /repos/acme/rules/commits/head/status": func(*http.Request) apiReply {
				return apiReply{Status: http.StatusForbidden, Body: `{"message":"denied"}`}
			},
		})
		metadata, err := git.ReadPullRequestMetadata(ctx, providerClient(models.ConnectionTypeGithub, api.Server.URL, nil).Provider,
			repoURL("https://github.com/acme/rules", ""), git.PullRequestMetadataOptions{Number: 9})
		Expect(err).To(HaveOccurred())
		Expect(metadata.HeadCommitURL).To(Equal("https://github.com/acme/rules/commit/head"))
		Expect(metadata.BaseBranchURL).To(Equal("https://github.com/acme/rules/tree/main"))
	})
	DescribeTable("links branches and immutable revisions in the correct repository",
		func(raw string, options git.WebRefOptions, expected string) {
			actual, err := repoURL(raw, "").RefWebURL(options)
			Expect(err).NotTo(HaveOccurred())
			Expect(actual).To(Equal(expected))
		},
		Entry("GitHub fork branch", "https://github.com/acme/rules", git.WebRefOptions{Repository: "contributor/rules", Ref: "feature/a b", Kind: git.RefBranch}, "https://github.com/contributor/rules/tree/feature%2Fa%20b"),
		Entry("GitLab commit", "https://gitlab.com/acme/rules", git.WebRefOptions{Ref: "abcdef", Kind: git.RefSHA}, "https://gitlab.com/acme/rules/-/commit/abcdef"),
		Entry("Azure target branch", "https://dev.azure.com/acme/Platform/_git/rules", git.WebRefOptions{Ref: "release/qa", Kind: git.RefBranch}, "https://dev.azure.com/acme/Platform/_git/rules?version=GBrelease%2Fqa"),
		Entry("Azure fork commit", "https://dev.azure.com/acme/Platform/_git/rules", git.WebRefOptions{Repository: "Other/fork", Ref: "abcdef", Kind: git.RefSHA}, "https://dev.azure.com/acme/Other/_git/fork/commit/abcdef"),
	)

	It("reads Azure avatars, mergeability and checks without confusing clean merges with successful checks", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/Platform/_apis/git/repositories/rules/pullrequests/9":           reply(`{"pullRequestId":9,"status":"active","mergeStatus":"succeeded","sourceRefName":"refs/heads/feature/a","targetRefName":"refs/heads/main","createdBy":{"displayName":"Ada","imageUrl":"https://example.com/avatar.png"},"lastMergeSourceCommit":{"commitId":"head"},"repository":{"name":"rules","project":{"name":"Platform"}}}`),
			"GET /acme/Platform/_apis/git/repositories/rules/pullrequests/9/statuses":  reply(`{"value":[{"id":1,"state":"failed","context":{"genre":"ci","name":"build"},"targetUrl":"https://example.com/build/1"}]}`),
			"GET /acme/_apis/projects/Platform":                                        reply(`{"id":"project-id"}`),
			"GET /acme/Platform/_apis/policy/evaluations":                              reply(`{"value":[]}`),
			"GET /acme/Platform/_apis/git/repositories/rules/pullrequests/9/reviewers": reply(`{"value":[]}`),
		})
		provider := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider
		metadata, err := git.ReadPullRequestMetadata(ctx, provider, repoURL("https://dev.azure.com/acme/Platform/_git/rules", ""), git.PullRequestMetadataOptions{Number: 9, BaseSHA: "base"})
		Expect(err).NotTo(HaveOccurred())
		Expect(metadata.Author).To(Equal("Ada"))
		Expect(metadata.AuthorAvatarURL).To(Equal("https://example.com/avatar.png"))
		Expect(metadata.BaseSHA).To(Equal("base"))
		Expect(metadata.Mergeability).To(Equal("mergeable"))
		Expect(metadata.Checks).To(ContainElement(git.PullRequestCheck{Name: "ci/build", State: "failure", URL: "https://example.com/build/1"}))
		Expect(metadata.HeadBranchURL).To(ContainSubstring("version=GBfeature%2Fa"))
		Expect(metadata.BaseCommitURL).To(HaveSuffix("/commit/base"))
	})
})
