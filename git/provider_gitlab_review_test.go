// Tests reading a GitLab merge request's review: its required approvals as a policy and each
// reviewer's state as a vote.
package git_test

import (
	"net/http"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("GitLab merge request review", func() {
	const mr = "/api/v4/projects/acme%2Fapi/merge_requests/5"
	api := func() git.RepoURL {
		return repoURL("https://gitlab.example.com/acme/api.git", models.ConnectionTypeGitlab)
	}
	review := func(ctx SpecContext, approvals, reviewers string) git.PullRequestReview {
		fake := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET " + mr + "/approvals": reply(approvals),
			"GET " + mr + "/reviewers": reply(reviewers),
		})
		reader, ok := providerClient(models.ConnectionTypeGitlab, fake.Server.URL, nil).Provider.(git.PullRequestReviewReader)
		Expect(ok).To(BeTrue())
		read, err := reader.PullRequestReview(ctx, api(), 5)
		Expect(err).ToNot(HaveOccurred())
		return read
	}

	It("is its required approvals and each reviewer's state as a vote", func(ctx SpecContext) {
		Expect(review(ctx, `{"approved":false,"approvals_required":2,"approvals_left":1,"approved_by":[{"user":{"id":9,"name":"Lin"}}]}`,
			`[{"user":{"id":7,"name":"Grace Hopper"},"state":"requested_changes"},{"user":{"id":8,"name":"Ada Lovelace"},"state":"unreviewed"},
			  {"user":{"id":9,"name":"Lin"},"state":"approved"}]`)).To(Equal(git.PullRequestReview{
			Policies: []git.PullRequestPolicy{{Name: "Approvals", Status: "rejected", Required: true, MinimumApproverCount: 2}},
			Reviewers: []git.PullRequestReviewer{
				{ID: "7", Name: "Grace Hopper", Vote: -10}, {ID: "8", Name: "Ada Lovelace", Vote: 0}, {ID: "9", Name: "Lin", Vote: 10},
			},
		}))
	})

	It("counts an approver who wasn't asked to review, and has no policy when none is required", func(ctx SpecContext) {
		Expect(review(ctx, `{"approved":true,"approvals_required":0,"approvals_left":0,"approved_by":[{"user":{"id":9,"name":"Lin"}}]}`, `[]`)).
			To(Equal(git.PullRequestReview{Policies: []git.PullRequestPolicy{}, Reviewers: []git.PullRequestReviewer{{ID: "9", Name: "Lin", Vote: 10}}}))
	})
})
