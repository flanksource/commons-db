package git_test

import (
	"fmt"
	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"net/http"
	"strings"
)

var _ = Describe("remote ancestry", func() {
	DescribeTable("preserves provider errors", func(ctx SpecContext, status int, body string, want string) {
		base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
		path := "/repos/acme/rules/compare/" + base + "..." + head
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET " + path:                           func(*http.Request) apiReply { return apiReply{Status: status, Body: body} },
			"GET /repos/acme/rules/commits/" + head: reply(`{"sha":"` + head + `"}`),
		})
		provider := providerClient(models.ConnectionTypeGithub, api.Server.URL, nil).Provider.(git.RemoteHistoryProvider)
		_, err := provider.RemoteMergeBase(ctx, git.RemoteMergeBaseOptions{Repository: git.RepoURL{Owner: "acme", Name: "rules"}, BaseSHA: base, HeadSHA: head})
		Expect(err).To(MatchError(ContainSubstring(want)))
	}, Entry("unauthorized", 401, `{}`, "HTTP 401"), Entry("rate limited", 429, `{}`, "HTTP 429"), Entry("invalid successful response", 200, `{}`, "invalid merge-base SHA"), Entry("unknown candidate", 404, `{}`, "candidate is unknown"))
	DescribeTable("reads a metadata-only merge base", func(ctx SpecContext, kind, path, response string) {
		base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
		api := newFakeAPI(map[string]func(*http.Request) apiReply{"GET " + path: func(r *http.Request) apiReply {
			switch kind {
			case models.ConnectionTypeGithub:
				Expect(r.URL.Query().Get("page")).To(Equal("2"))
				Expect(r.URL.Query().Get("per_page")).To(Equal("1"))
			case models.ConnectionTypeAzureDevops:
				Expect(r.URL.Query().Get("$top")).To(Equal("0"))
				Expect(r.URL.Query().Get("baseVersion")).To(Equal(base))
			case models.ConnectionTypeGitlab:
				Expect(r.URL.Query()["refs[]"]).To(Equal([]string{base, head}))
			}
			return apiReply{Body: fmt.Sprintf(response, base)}
		}})
		provider := providerClient(kind, api.Server.URL, nil).Provider.(git.RemoteHistoryProvider)
		repo := git.RepoURL{Owner: "acme", Name: "rules", Project: "project"}
		actual, err := provider.RemoteMergeBase(ctx, git.RemoteMergeBaseOptions{Repository: repo, BaseSHA: base, HeadSHA: head})
		Expect(err).NotTo(HaveOccurred())
		Expect(actual).To(Equal(base))
	},
		Entry("azure", models.ConnectionTypeAzureDevops, "/project/_apis/git/repositories/rules/diffs/commits", `{"commonCommit":"%s"}`),
		Entry("github", models.ConnectionTypeGithub, "/repos/acme/rules/compare/"+strings.Repeat("a", 40)+"..."+strings.Repeat("b", 40), `{"merge_base_commit":{"sha":"%s"}}`),
		Entry("gitlab", models.ConnectionTypeGitlab, "/api/v4/projects/acme%2Frules/repository/merge_base", `{"id":"%s"}`),
	)
})
