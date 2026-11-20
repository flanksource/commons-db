// Tests writing to a pull request's threads: reading one, opening one (on a file's lines of the diff
// it was written on), replying in it, and resolving or reopening it.
package git_test

import (
	"net/http"
	"time"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Writing an Azure DevOps pull request's threads", func() {
	const base = "/acme/Platform/_apis/git/repositories/rules/pullrequests/9"
	rules := func() git.RepoURL { return repoURL("https://dev.azure.com/acme/Platform/_git/rules", "") }
	writer := func(api *fakeAPI) git.PullRequestThreadWriter {
		writer, ok := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider.(git.PullRequestThreadWriter)
		Expect(ok).To(BeTrue())
		return writer
	}
	thread := `{"id":5,"status":1,"lastUpdatedDate":"2026-11-02T12:05:00Z","threadContext":{"filePath":"/rules.go","rightFileStart":{"line":12}},"comments":[
		{"id":1,"content":"Split this up.","commentType":"text","publishedDate":"2026-11-02T12:00:00Z","author":{"id":"grace-id","displayName":"Grace Hopper","uniqueName":"grace@example.com"}},
		{"id":2,"parentCommentId":1,"content":"Will do.","commentType":"text","publishedDate":"2026-11-02T12:05:00Z","author":{"id":"ada-id","displayName":"Ada Lovelace"}}]}`

	It("reads one thread with its status, its comments' parents and their authors", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{"GET " + base + "/threads/5": reply(thread)})
		read, err := writer(api).PullRequestThread(ctx, rules(), 9, "5")
		Expect(err).ToNot(HaveOccurred())
		Expect(read).To(Equal(git.PullRequestThread{ID: "5", Status: "active", Path: "/rules.go", Line: 12,
			URL: "https://dev.azure.com/acme/Platform/_git/rules/pullrequest/9?discussionId=5", UpdatedAt: time.Date(2026, 11, 2, 12, 5, 0, 0, time.UTC),
			Comments: []git.PullRequestThreadComment{
				{ID: "1", Author: "Grace Hopper", AuthorID: "grace-id", AuthorLogin: "grace@example.com", Body: "Split this up.", CreatedAt: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)},
				{ID: "2", ParentID: "1", Author: "Ada Lovelace", AuthorID: "ada-id", Body: "Will do.", CreatedAt: time.Date(2026, 11, 2, 12, 5, 0, 0, time.UTC)},
			}}))
	})

	It("opens a general thread", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{"POST " + base + "/threads": reply(`{"id":6,"status":"active","comments":[
			{"id":1,"content":"Looks good.","commentType":"text","author":{"displayName":"Ada Lovelace"}}]}`)})
		opened, err := writer(api).CreatePullRequestThread(ctx, rules(), 9, git.PullRequestThreadRequest{Body: "Looks good."})
		Expect(err).ToNot(HaveOccurred())
		Expect(opened.ID).To(Equal("6"))
		Expect(api.Calls()[0].Body).To(Equal(map[string]any{"status": "active",
			"comments": []any{map[string]any{"content": "Looks good.", "parentCommentId": float64(0), "commentType": float64(1)}}}))
	})

	It("opens a thread on the lines of the diff it was written on", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET " + base: reply(`{"pullRequestId":9,"status":"active","sourceRefName":"refs/heads/a","targetRefName":"refs/heads/main",
				"lastMergeSourceCommit":{"commitId":"` + headSHA + `"},"repository":{"name":"rules","project":{"name":"Platform"}}}`),
			"GET " + base + "/iterations": reply(`{"value":[{"id":1,"sourceRefCommit":{"commitId":"old"},"commonRefCommit":{"commitId":"` + mergeSHA + `"}},
				{"id":2,"sourceRefCommit":{"commitId":"` + headSHA + `"},"commonRefCommit":{"commitId":"` + mergeSHA + `"}}]}`),
			"GET " + base + "/iterations/2/changes": reply(`{"changeEntries":[{"changeTrackingId":3,"item":{"path":"/README.md"}},
				{"changeTrackingId":4,"item":{"path":"/rules.go"}}],"nextSkip":0,"nextTop":0}`),
			"POST " + base + "/threads": reply(`{"id":7,"status":"active","threadContext":{"filePath":"/rules.go","rightFileStart":{"line":12}},"comments":[
				{"id":1,"content":"Rename this.","commentType":"text","author":{"displayName":"Ada Lovelace"}}]}`),
		})
		opened, err := writer(api).CreatePullRequestThread(ctx, rules(), 9, git.PullRequestThreadRequest{Body: "Rename this.",
			Path: "rules.go", StartLine: 12, EndLine: 14, HeadSHA: headSHA, BaseSHA: mergeSHA})
		Expect(err).ToNot(HaveOccurred())
		Expect(opened.Path).To(Equal("/rules.go"))
		body := api.Calls()[3].Body
		Expect(body["threadContext"]).To(Equal(map[string]any{"filePath": "/rules.go",
			"rightFileStart": map[string]any{"line": float64(12), "offset": float64(0)}, "rightFileEnd": map[string]any{"line": float64(14), "offset": float64(0)}}))
		Expect(body["pullRequestThreadContext"]).To(Equal(map[string]any{"changeTrackingId": float64(4),
			"iterationContext": map[string]any{"firstComparingIteration": float64(0), "secondComparingIteration": float64(2)}}))
	})

	It("refuses a thread on lines of a diff the pull request moved past", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET " + base: reply(`{"pullRequestId":9,"status":"active","sourceRefName":"refs/heads/a","targetRefName":"refs/heads/main",
				"lastMergeSourceCommit":{"commitId":"` + staleSHA + `"},"repository":{"name":"rules","project":{"name":"Platform"}}}`),
		})
		_, err := writer(api).CreatePullRequestThread(ctx, rules(), 9, git.PullRequestThreadRequest{Body: "Rename this.",
			Path: "rules.go", StartLine: 12, EndLine: 12, HeadSHA: headSHA, BaseSHA: mergeSHA})
		Expect(err).To(MatchError(git.ErrThreadTargetMoved))
	})

	It("replies in a thread under the comment named", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{"POST " + base + "/threads/5/comments": reply(`{"id":3,"parentCommentId":1,
			"content":"Done.","commentType":"text","publishedDate":"2026-11-02T13:00:00Z","author":{"displayName":"Ada Lovelace"}}`)})
		replied, err := writer(api).ReplyPullRequestThread(ctx, rules(), 9, "5", "1", "Done.")
		Expect(err).ToNot(HaveOccurred())
		Expect(replied).To(Equal(git.PullRequestThreadComment{ID: "3", ParentID: "1", Author: "Ada Lovelace", Body: "Done.",
			CreatedAt: time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC)}))
		Expect(api.Calls()[0].Body).To(Equal(map[string]any{"content": "Done.", "parentCommentId": float64(1), "commentType": float64(1)}))
	})

	It("resolves and reopens a thread, refusing a state Azure didn't take", func(ctx SpecContext) {
		status := `"fixed"`
		api := newFakeAPI(map[string]func(*http.Request) apiReply{"PATCH " + base + "/threads/5": func(*http.Request) apiReply {
			return reply(`{"id":5,"status":` + status + `,"comments":[{"id":1,"content":"Split this up.","commentType":"text","author":{"displayName":"Grace Hopper"}}]}`)(nil)
		}})
		resolved, err := writer(api).ResolvePullRequestThread(ctx, rules(), 9, "5", true)
		Expect(err).ToNot(HaveOccurred())
		Expect(resolved.Resolved).To(BeTrue())
		Expect(api.Calls()[0].Body).To(Equal(map[string]any{"status": "fixed"}))

		status = `"active"`
		reopened, err := writer(api).ResolvePullRequestThread(ctx, rules(), 9, "5", false)
		Expect(err).ToNot(HaveOccurred())
		Expect(reopened.Resolved).To(BeFalse())
		Expect(api.Calls()[1].Body).To(Equal(map[string]any{"status": "active"}))

		_, err = writer(api).ResolvePullRequestThread(ctx, rules(), 9, "5", true)
		Expect(err).To(MatchError(`azure DevOps left pull request 9 thread 5 active instead of resolving it`))
	})
})
