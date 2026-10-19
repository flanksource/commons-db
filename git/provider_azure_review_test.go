package git_test

import (
	"net/http"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Azure pull request review", func() {
	It("reports blocking policy failures, their build, and missing required reviewer votes", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/_apis/projects/Platform":                                        reply(`{"id":"11111111-1111-1111-1111-111111111111"}`),
			"GET /acme/Platform/_apis/policy/evaluations":                              reply(`{"count":2,"value":[{"evaluationId":"build","status":"rejected","configuration":{"id":4,"isBlocking":true,"isEnabled":true,"type":{"displayName":"Build"},"settings":{"displayName":"PR validation"}},"context":{"buildId":126932}},{"evaluationId":"review","status":"rejected","configuration":{"id":5,"isBlocking":true,"isEnabled":true,"type":{"displayName":"Minimum number of reviewers"},"settings":{"minimumApproverCount":2}}}]}`),
			"GET /acme/Platform/_apis/git/repositories/rules/pullrequests/9/reviewers": reply(`{"count":2,"value":[{"id":"r1","displayName":"Ada","isRequired":true,"vote":0},{"id":"r2","displayName":"Lin","isRequired":false,"vote":10}]}`),
		})
		provider := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider
		review, err := provider.(git.PullRequestReviewReader).PullRequestReview(ctx, repoURL("https://dev.azure.com/acme/Platform/_git/rules", ""), 9)
		Expect(err).ToNot(HaveOccurred())
		Expect(review).To(Equal(git.PullRequestReview{
			Policies: []git.PullRequestPolicy{
				{ID: 4, Name: "PR validation", Status: "rejected", Required: true, BuildID: 126932, URL: "https://dev.azure.com/acme/Platform/_build/results?buildId=126932&view=results"},
				{ID: 5, Name: "Minimum number of reviewers", Status: "rejected", Required: true, MinimumApproverCount: 2},
			},
			Reviewers: []git.PullRequestReviewer{
				{ID: "r1", Name: "Ada", Required: true, Vote: 0},
				{ID: "r2", Name: "Lin", Required: false, Vote: 10},
			},
		}))
		Expect(api.Calls()[1].Query).To(ContainSubstring("artifactId=vstfs%3A%2F%2F%2FCodeReview%2FCodeReviewId%2F11111111-1111-1111-1111-111111111111%2F9"))
	})
})
