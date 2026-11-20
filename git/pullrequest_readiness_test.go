// Tests whether an Azure DevOps pull request can merge now: its merge computation, draft state,
// blocking policies, and whether that computation has caught up with its branches.
package git_test

import (
	"net/http"
	"strings"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("An Azure DevOps pull request's merge readiness", func() {
	const repoBase = "/acme/Platform/_apis/git/repositories"
	rules := func() git.RepoURL { return repoURL("https://dev.azure.com/acme/Platform/_git/rules", "") }
	pull := func(status, mergeStatus, extra string) string {
		return `{"pullRequestId":9,"status":"` + status + `","mergeStatus":"` + mergeStatus + `","sourceRefName":"refs/heads/tighten",
			"targetRefName":"refs/heads/main","lastMergeSourceCommit":{"commitId":"` + headSHA + `"},"lastMergeTargetCommit":{"commitId":"` + mergeSHA + `"},` + extra +
			`"repository":{"id":"rules-id","name":"rules","project":{"name":"Platform"}}}`
	}
	refs := func(head, target string) func(*http.Request) apiReply {
		return func(r *http.Request) apiReply {
			if strings.Contains(r.URL.Query().Get("filter"), "tighten") {
				return apiReply{Body: `{"value":[{"name":"refs/heads/tighten","objectId":"` + head + `"}]}`}
			}
			return apiReply{Body: `{"value":[{"name":"refs/heads/main","objectId":"` + target + `"}]}`}
		}
	}
	routes := func(pr, policies string, head, target string) map[string]func(*http.Request) apiReply {
		return map[string]func(*http.Request) apiReply{
			"GET " + repoBase + "/rules/pullrequests/9":           reply(pr),
			"GET /acme/_apis/projects/Platform":                   reply(`{"id":"platform-id"}`),
			"GET /acme/Platform/_apis/policy/evaluations":         reply(`{"value":[` + policies + `]}`),
			"GET " + repoBase + "/rules/pullrequests/9/reviewers": reply(`{"value":[]}`),
			"GET " + repoBase + "/rules-id/refs":                  refs(head, target),
		}
	}
	policy := func(id int, typeID, name, status string) string {
		return `{"status":"` + status + `","configuration":{"id":` + itoa(id) + `,"isBlocking":true,"isEnabled":true,
			"type":{"id":"` + typeID + `","displayName":"` + name + `"}},"context":{}}`
	}
	readiness := func(ctx SpecContext, api *fakeAPI) git.MergeReadiness {
		GinkgoHelper()
		reader, ok := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider.(git.PullRequestReadinessReader)
		Expect(ok).To(BeTrue())
		read, err := reader.PullRequestReadiness(ctx, rules(), 9)
		Expect(err).ToNot(HaveOccurred())
		return read
	}
	const build = "0609b952-1397-4640-95ec-e00a01b2c241"

	It("is ready when the merge succeeded, its branches haven't moved and no blocking policy waits", func(ctx SpecContext) {
		api := newFakeAPI(routes(pull("active", "succeeded", ""), policy(3, "fa4e907d-c16b-4a4c-9dfa-4906e5d171dd", "Minimum reviewers", "approved"), headSHA, mergeSHA))
		Expect(readiness(ctx, api)).To(Equal(git.MergeReadiness{State: "ready"}))
	})

	It("is conflicting with the target branch's conflicts and the merge failure", func(ctx SpecContext) {
		api := newFakeAPI(routes(pull("active", "conflicts", `"mergeFailureMessage":"rules.go conflicts",`), "", headSHA, mergeSHA))
		Expect(readiness(ctx, api)).To(Equal(git.MergeReadiness{State: "conflicting", Reasons: []string{"conflicts with target branch", "rules.go conflicts"}}))
	})

	It("waits for running builds, and is blocked by a draft or a rejected policy", func(ctx SpecContext) {
		api := newFakeAPI(routes(pull("active", "succeeded", ""), policy(7, build, "Build", "running"), headSHA, mergeSHA))
		Expect(readiness(ctx, api)).To(Equal(git.MergeReadiness{State: "pending", WaitingForBuilds: true, Reasons: []string{"Build (policy 7): running"}}))

		api = newFakeAPI(routes(pull("active", "succeeded", `"isDraft":true,`), policy(3, "fa4e907d", "Minimum reviewers", "rejected"), headSHA, mergeSHA))
		Expect(readiness(ctx, api)).To(Equal(git.MergeReadiness{State: "blocked", Reasons: []string{"PR is a draft", "Minimum reviewers (policy 3): rejected"}}))
	})

	It("waits for the merge computation when a branch moved past it", func(ctx SpecContext) {
		api := newFakeAPI(routes(pull("active", "succeeded", ""), "", staleSHA, mergeSHA))
		Expect(readiness(ctx, api)).To(Equal(git.MergeReadiness{State: "pending", WaitingForMerge: true,
			Reasons: []string{"merge computation has not caught up to the source or target branch"}}))
	})

	It("has none for a pull request that isn't open", func(ctx SpecContext) {
		api := newFakeAPI(routes(pull("completed", "succeeded", ""), "", headSHA, mergeSHA))
		Expect(readiness(ctx, api)).To(Equal(git.MergeReadiness{}))
	})
})
