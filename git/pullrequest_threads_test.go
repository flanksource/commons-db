// Tests reading a pull request's discussion threads: their comments, whether each is resolved,
// and the file and line a review thread is on.
package git_test

import (
	"net/http"
	"time"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Azure DevOps pull request threads", func() {
	It("reads each live thread's text comments, resolution and position", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/Platform/_apis/git/repositories/rules/pullrequests/9/threads": reply(`{"value":[
				{"id":5,"status":"active","threadContext":{"filePath":"/rules.go","rightFileStart":{"line":12}},"comments":[
					{"id":1,"content":"Split this up.","commentType":"text","publishedDate":"2026-11-02T12:00:00Z",
					 "author":{"displayName":"Grace Hopper","uniqueName":"grace@example.com","imageUrl":"https://avatars.example/grace"}},
					{"id":2,"content":"","commentType":"text","isDeleted":true,"publishedDate":"2026-11-02T12:01:00Z","author":{"displayName":"Grace Hopper"}}]},
				{"id":6,"status":4,"comments":[{"id":1,"content":"Looks good.","commentType":"text","publishedDate":"2026-11-02T13:00:00Z","author":{"displayName":"Ada Lovelace"}}]},
				{"id":7,"comments":[{"id":1,"content":"Updated the PR","commentType":"system","publishedDate":"2026-11-02T13:05:00Z","author":{"displayName":"Azure"}}]},
				{"id":8,"status":"fixed","isDeleted":true,"comments":[{"id":1,"content":"gone","commentType":"text","author":{"displayName":"Ada Lovelace"}}]}]}`),
		})
		reader, ok := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider.(git.PullRequestThreadReader)
		Expect(ok).To(BeTrue())

		threads, err := reader.PullRequestThreads(ctx, repoURL("https://dev.azure.com/acme/Platform/_git/rules", ""), 9)
		Expect(err).ToNot(HaveOccurred())
		Expect(threads).To(Equal([]git.PullRequestThread{
			{ID: "5", Path: "/rules.go", Line: 12, URL: "https://dev.azure.com/acme/Platform/_git/rules/pullrequest/9?discussionId=5",
				Comments: []git.PullRequestThreadComment{{ID: "1", Author: "Grace Hopper", AuthorLogin: "grace@example.com",
					AuthorAvatarURL: "https://avatars.example/grace", Body: "Split this up.", CreatedAt: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}}},
			{ID: "6", Resolved: true, URL: "https://dev.azure.com/acme/Platform/_git/rules/pullrequest/9?discussionId=6",
				Comments: []git.PullRequestThreadComment{{ID: "1", Author: "Ada Lovelace", Body: "Looks good.", CreatedAt: time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC)}}},
		}), "a system note's thread and a deleted thread have no comments to read")
	})

	It("refuses a thread status it doesn't know", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/Platform/_apis/git/repositories/rules/pullrequests/9/threads": reply(`{"value":[{"id":5,"status":"reopened","comments":[
				{"id":1,"content":"Hm.","commentType":"text","author":{"displayName":"Grace Hopper"}}]}]}`),
		})
		reader := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider.(git.PullRequestThreadReader)
		_, err := reader.PullRequestThreads(ctx, repoURL("https://dev.azure.com/acme/Platform/_git/rules", ""), 9)
		Expect(err).To(MatchError(ContainSubstring(`azure DevOps pull request 9 thread 5 has unknown status "reopened"`)))
	})
})
