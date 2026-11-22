// Tests a GitLab merge request's discussions as threads: their notes, resolution and position,
// and opening, replying in, resolving and reopening them.
package git_test

import (
	"net/http"
	"time"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("GitLab merge request discussions", func() {
	const base = "/api/v4/projects/acme%2Fapi/merge_requests/5"
	const web = "https://gitlab.example.com/acme/api/-/merge_requests/5"
	api := func() git.RepoURL {
		return repoURL("https://gitlab.example.com/acme/api.git", models.ConnectionTypeGitlab)
	}
	provider := func(fake *fakeAPI) git.Provider {
		return providerClient(models.ConnectionTypeGitlab, fake.Server.URL, nil).Provider
	}
	review := `{"id":"d1","individual_note":false,"notes":[
		{"id":11,"type":"DiffNote","body":"Split this up.","system":false,"resolvable":true,"resolved":false,"created_at":"2026-11-03T12:00:00Z","updated_at":"2026-11-03T12:00:00Z",
		 "author":{"id":7,"name":"Grace Hopper","username":"grace","avatar_url":"https://avatars.example/grace"},
		 "position":{"new_path":"api.go","new_line":12,"old_path":"api.go","old_line":null}},
		{"id":12,"type":"DiffNote","body":"Will do.","system":false,"resolvable":true,"resolved":false,"created_at":"2026-11-03T12:05:00Z","updated_at":"2026-11-03T12:06:00Z",
		 "author":{"id":8,"name":"Ada Lovelace","username":"ada"}}]}`
	grace := git.PullRequestThreadComment{ID: "11", Author: "Grace Hopper", AuthorID: "7", AuthorLogin: "grace",
		AuthorAvatarURL: "https://avatars.example/grace", Body: "Split this up.", CreatedAt: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	ada := git.PullRequestThreadComment{ID: "12", ParentID: "11", Author: "Ada Lovelace", AuthorID: "8", AuthorLogin: "ada",
		Body: "Will do.", CreatedAt: time.Date(2026, 11, 3, 12, 5, 0, 0, time.UTC)}
	reviewThread := git.PullRequestThread{ID: "d1", Status: "unresolved", Path: "api.go", Line: 12, URL: web + "#note_11",
		UpdatedAt: time.Date(2026, 11, 3, 12, 6, 0, 0, time.UTC), Comments: []git.PullRequestThreadComment{grace, ada}}

	It("reads each discussion someone wrote in, with its resolution and position, and leaves system notes out", func(ctx SpecContext) {
		fake := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET " + base + "/discussions": reply(`[` + review + `,
				{"id":"d2","individual_note":true,"notes":[{"id":21,"type":null,"body":"Looks good.","system":false,"resolvable":false,
				 "created_at":"2026-11-03T13:00:00Z","updated_at":"2026-11-03T13:00:00Z","author":{"id":8,"name":"Ada Lovelace","username":"ada"}}]},
				{"id":"d3","individual_note":true,"notes":[{"id":31,"type":null,"body":"added 1 commit","system":true,"resolvable":false,
				 "created_at":"2026-11-03T13:05:00Z","author":{"id":8,"name":"Ada Lovelace","username":"ada"}}]},
				{"id":"d4","individual_note":false,"notes":[{"id":41,"type":"DiscussionNote","body":"Rename?","system":false,"resolvable":true,"resolved":true,
				 "created_at":"2026-11-03T14:00:00Z","updated_at":"2026-11-03T14:10:00Z","author":{"id":7,"name":"Grace Hopper","username":"grace"},
				 "position":{"new_path":"old.go","new_line":null,"old_path":"old.go","old_line":4}}]}]`),
		})
		reader, ok := provider(fake).(git.PullRequestThreadReader)
		Expect(ok).To(BeTrue())
		threads, err := reader.PullRequestThreads(ctx, api(), 5)
		Expect(err).ToNot(HaveOccurred())
		Expect(threads).To(Equal([]git.PullRequestThread{
			reviewThread,
			{ID: "d2", URL: web + "#note_21", UpdatedAt: time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC), Comments: []git.PullRequestThreadComment{
				{ID: "21", Author: "Ada Lovelace", AuthorID: "8", AuthorLogin: "ada", Body: "Looks good.", CreatedAt: time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC)}}},
			{ID: "d4", Resolved: true, Status: "resolved", Path: "old.go", Line: 4, URL: web + "#note_41", UpdatedAt: time.Date(2026, 11, 3, 14, 10, 0, 0, time.UTC),
				Comments: []git.PullRequestThreadComment{{ID: "41", Author: "Grace Hopper", AuthorID: "7", AuthorLogin: "grace", Body: "Rename?",
					CreatedAt: time.Date(2026, 11, 3, 14, 0, 0, 0, time.UTC)}}},
		}), "an individual note has no status; a note on the old side is placed on its old line")
	})

	writer := func(fake *fakeAPI) git.PullRequestThreadWriter {
		writer, ok := provider(fake).(git.PullRequestThreadWriter)
		Expect(ok).To(BeTrue())
		return writer
	}

	It("reads one discussion", func(ctx SpecContext) {
		fake := newFakeAPI(map[string]func(*http.Request) apiReply{"GET " + base + "/discussions/d1": reply(review)})
		read, err := writer(fake).PullRequestThread(ctx, api(), 5, "d1")
		Expect(err).ToNot(HaveOccurred())
		Expect(read).To(Equal(reviewThread))
	})

	It("opens a general discussion", func(ctx SpecContext) {
		fake := newFakeAPI(map[string]func(*http.Request) apiReply{"POST " + base + "/discussions": reply(`{"id":"d5","notes":[
			{"id":51,"body":"Looks good.","resolvable":true,"resolved":false,"author":{"id":8,"name":"Ada Lovelace","username":"ada"}}]}`)})
		opened, err := writer(fake).CreatePullRequestThread(ctx, api(), 5, git.PullRequestThreadRequest{Body: "Looks good."})
		Expect(err).ToNot(HaveOccurred())
		Expect(opened.ID).To(Equal("d5"))
		Expect(fake.Calls()[0].Body).To(Equal(map[string]any{"body": "Looks good."}))
	})

	mergeRequest := reply(`{"iid":5,"state":"opened","source_branch":"fast","target_branch":"main","sha":"` + headSHA + `",
		"source_project_id":1,"target_project_id":1,"diff_refs":{"base_sha":"` + mergeSHA + `","head_sha":"` + headSHA + `","start_sha":"` + staleSHA + `"}}`)
	// api.go, renamed from server.go: old lines 10-12 are a, b := 2, return; new lines 10-13 are a,
	// b := 3, c := 4, return.
	diffs := func(*http.Request) apiReply {
		return apiReply{Headers: map[string]string{"X-Next-Page": "2"}, Body: `[{"old_path":"README.md","new_path":"README.md","diff":"@@ -1 +1 @@\n-a\n+b\n"}]`}
	}
	secondDiffs := reply(`[{"old_path":"server.go","new_path":"api.go","renamed_file":true,
		"diff":"@@ -10,3 +10,4 @@ func api() {\n \ta := 1\n-\tb := 2\n+\tb := 3\n+\tc := 4\n \treturn\n\\ No newline at end of file\n"}]`)
	diffRoutes := func() map[string]func(*http.Request) apiReply {
		return map[string]func(*http.Request) apiReply{
			"GET " + base: mergeRequest,
			"GET " + base + "/diffs": func(r *http.Request) apiReply {
				if r.URL.Query().Get("page") == "2" {
					return secondDiffs(r)
				}
				return diffs(r)
			},
			"POST " + base + "/discussions": reply(`{"id":"d6","notes":[{"id":61,"type":"DiffNote","body":"Rename this.","resolvable":true,
				"author":{"id":8,"name":"Ada Lovelace","username":"ada"},"position":{"new_path":"api.go","new_line":13}}]}`),
		}
	}
	// lineCode is GitLab's name for a diff line of api.go: its path's SHA-1, old line and new line.
	lineCode := func(old, current string) string {
		return "f7e02cab0db787f60e7f5e727806f0f8b6c6d72a_" + old + "_" + current
	}
	posted := func(fake *fakeAPI) map[string]any {
		calls := fake.Calls()
		return calls[len(calls)-1].Body["position"].(map[string]any)
	}

	It("opens a discussion on every line written on, each end named by its line code in the file's diff", func(ctx SpecContext) {
		fake := newFakeAPI(diffRoutes())
		opened, err := writer(fake).CreatePullRequestThread(ctx, api(), 5, git.PullRequestThreadRequest{Body: "Rename this.",
			Path: "/api.go", StartLine: 10, EndLine: 13, HeadSHA: headSHA, BaseSHA: mergeSHA})
		Expect(err).ToNot(HaveOccurred())
		Expect(opened.Line).To(Equal(13))
		Expect(posted(fake)).To(Equal(map[string]any{
			"position_type": "text", "base_sha": mergeSHA, "head_sha": headSHA, "start_sha": staleSHA,
			"old_path": "server.go", "new_path": "api.go", "old_line": float64(12), "new_line": float64(13),
			"line_range": map[string]any{
				"start": map[string]any{"line_code": lineCode("10", "10"), "type": "old", "old_line": float64(10), "new_line": float64(10)},
				"end":   map[string]any{"line_code": lineCode("12", "13"), "type": "old", "old_line": float64(12), "new_line": float64(13)},
			}}))

		_, err = writer(fake).CreatePullRequestThread(ctx, api(), 5, git.PullRequestThreadRequest{Body: "Was this needed?",
			Path: "api.go", StartLine: 11, EndLine: 12, Old: true, HeadSHA: headSHA, BaseSHA: mergeSHA})
		Expect(err).ToNot(HaveOccurred())
		Expect(posted(fake)).To(HaveKeyWithValue("line_range", map[string]any{
			"start": map[string]any{"line_code": lineCode("11", "11"), "type": "old", "old_line": float64(11)},
			"end":   map[string]any{"line_code": lineCode("12", "13"), "type": "old", "old_line": float64(12), "new_line": float64(13)},
		}))
	})

	DescribeTable("places a note on one line by its side: an added line by its new number, a removed one by its old, an unchanged one by both",
		func(ctx SpecContext, line int, old bool, want map[string]any) {
			fake := newFakeAPI(diffRoutes())
			_, err := writer(fake).CreatePullRequestThread(ctx, api(), 5, git.PullRequestThreadRequest{Body: "Rename this.",
				Path: "api.go", StartLine: line, EndLine: line, Old: old, HeadSHA: headSHA, BaseSHA: mergeSHA})
			Expect(err).ToNot(HaveOccurred())
			position := posted(fake)
			Expect(position).ToNot(HaveKey("line_range"))
			delete(position, "position_type")
			delete(position, "base_sha")
			delete(position, "head_sha")
			delete(position, "start_sha")
			delete(position, "old_path")
			delete(position, "new_path")
			Expect(position).To(Equal(want))
		},
		Entry("added", 11, false, map[string]any{"new_line": float64(11)}),
		Entry("removed", 11, true, map[string]any{"old_line": float64(11)}),
		Entry("unchanged", 13, false, map[string]any{"old_line": float64(12), "new_line": float64(13)}))

	It("refuses a line the file's diff doesn't show, and a file the merge request doesn't change", func(ctx SpecContext) {
		fake := newFakeAPI(diffRoutes())
		_, err := writer(fake).CreatePullRequestThread(ctx, api(), 5, git.PullRequestThreadRequest{Body: "Rename this.",
			Path: "api.go", StartLine: 2, EndLine: 13, HeadSHA: headSHA, BaseSHA: mergeSHA})
		Expect(err).To(MatchError("gitlab merge request !5's diff of api.go doesn't show new line 2"))
		_, err = writer(fake).CreatePullRequestThread(ctx, api(), 5, git.PullRequestThreadRequest{Body: "Rename this.",
			Path: "main.go", StartLine: 2, EndLine: 2, HeadSHA: headSHA, BaseSHA: mergeSHA})
		Expect(err).To(MatchError("gitlab merge request !5 doesn't change main.go"))
	})

	It("refuses a discussion on lines of a diff the merge request moved past", func(ctx SpecContext) {
		fake := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET " + base: reply(`{"iid":5,"state":"opened","sha":"` + staleSHA + `","source_project_id":1,"target_project_id":1,
				"diff_refs":{"base_sha":"` + mergeSHA + `","head_sha":"` + staleSHA + `","start_sha":"` + mergeSHA + `"}}`),
		})
		_, err := writer(fake).CreatePullRequestThread(ctx, api(), 5, git.PullRequestThreadRequest{Body: "Rename this.",
			Path: "api.go", StartLine: 12, EndLine: 12, HeadSHA: headSHA, BaseSHA: mergeSHA})
		Expect(err).To(MatchError(git.ErrThreadTargetMoved))
	})

	It("replies in a discussion", func(ctx SpecContext) {
		fake := newFakeAPI(map[string]func(*http.Request) apiReply{"POST " + base + "/discussions/d1/notes": reply(`{"id":13,"body":"Done.",
			"created_at":"2026-11-03T15:00:00Z","author":{"id":8,"name":"Ada Lovelace","username":"ada"}}`)})
		replied, err := writer(fake).ReplyPullRequestThread(ctx, api(), 5, "d1", "11", "Done.")
		Expect(err).ToNot(HaveOccurred())
		Expect(replied).To(Equal(git.PullRequestThreadComment{ID: "13", ParentID: "11", Author: "Ada Lovelace", AuthorID: "8", AuthorLogin: "ada",
			Body: "Done.", CreatedAt: time.Date(2026, 11, 3, 15, 0, 0, 0, time.UTC)}))
		Expect(fake.Calls()[0].Body).To(Equal(map[string]any{"body": "Done."}))
	})

	It("resolves and reopens a discussion, refusing a state GitLab didn't take", func(ctx SpecContext) {
		resolved := "true"
		fake := newFakeAPI(map[string]func(*http.Request) apiReply{"PUT " + base + "/discussions/d1": func(*http.Request) apiReply {
			return reply(`{"id":"d1","notes":[{"id":11,"body":"Split this up.","resolvable":true,"resolved":` + resolved + `,"author":{"name":"Grace Hopper"}}]}`)(nil)
		}})
		thread, err := writer(fake).ResolvePullRequestThread(ctx, api(), 5, "d1", true)
		Expect(err).ToNot(HaveOccurred())
		Expect(thread.Resolved).To(BeTrue())
		Expect(fake.Calls()[0].Query).To(Equal("resolved=true"))

		resolved = "false"
		thread, err = writer(fake).ResolvePullRequestThread(ctx, api(), 5, "d1", false)
		Expect(err).ToNot(HaveOccurred())
		Expect(thread.Resolved).To(BeFalse())
		Expect(fake.Calls()[1].Query).To(Equal("resolved=false"))

		_, err = writer(fake).ResolvePullRequestThread(ctx, api(), 5, "d1", true)
		Expect(err).To(MatchError(`gitlab left merge request !5 discussion d1 unresolved instead of resolving it`))
	})
})
