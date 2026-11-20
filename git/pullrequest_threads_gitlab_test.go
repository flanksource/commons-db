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

	It("opens a discussion on the last line written on, of the diff the merge request is at", func(ctx SpecContext) {
		fake := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET " + base: reply(`{"iid":5,"state":"opened","source_branch":"fast","target_branch":"main","sha":"` + headSHA + `",
				"source_project_id":1,"target_project_id":1,"diff_refs":{"base_sha":"` + mergeSHA + `","head_sha":"` + headSHA + `","start_sha":"` + staleSHA + `"}}`),
			"POST " + base + "/discussions": reply(`{"id":"d6","notes":[{"id":61,"type":"DiffNote","body":"Rename this.","resolvable":true,
				"author":{"id":8,"name":"Ada Lovelace","username":"ada"},"position":{"new_path":"api.go","new_line":14}}]}`),
		})
		opened, err := writer(fake).CreatePullRequestThread(ctx, api(), 5, git.PullRequestThreadRequest{Body: "Rename this.",
			Path: "/api.go", StartLine: 12, EndLine: 14, HeadSHA: headSHA, BaseSHA: mergeSHA})
		Expect(err).ToNot(HaveOccurred())
		Expect(opened.Line).To(Equal(14))
		Expect(fake.Calls()[1].Body).To(Equal(map[string]any{"body": "Rename this.", "position": map[string]any{
			"position_type": "text", "base_sha": mergeSHA, "head_sha": headSHA, "start_sha": staleSHA,
			"old_path": "api.go", "new_path": "api.go", "new_line": float64(14)}}))

		_, err = writer(fake).CreatePullRequestThread(ctx, api(), 5, git.PullRequestThreadRequest{Body: "Was this needed?",
			Path: "api.go", StartLine: 3, EndLine: 3, Old: true, HeadSHA: headSHA, BaseSHA: mergeSHA})
		Expect(err).ToNot(HaveOccurred())
		Expect(fake.Calls()[3].Body["position"]).To(HaveKeyWithValue("old_line", float64(3)))
		Expect(fake.Calls()[3].Body["position"]).ToNot(HaveKey("new_line"))
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
