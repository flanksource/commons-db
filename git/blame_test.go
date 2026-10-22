package git_test

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Cache.Blame", func() {
	var fixture bareRepo
	var cache *git.Cache
	var repo *git.Repository
	BeforeEach(func() {
		fixture = newBareRepo("blame")
		cache = &git.Cache{Root: GinkgoT().TempDir()}
		repo = newRepository(fixture.FileURL(), git.Auth{})
	})

	It("attributes supplied contents from the bare cache without creating a worktree", func(ctx SpecContext) {
		const original = "<Rule>\n<Value/>\n</Rule>\n"
		created := fixture.commitOn(fixtureCommit{Branch: "main", Path: "rule.xml", Content: original, Message: "Add rule"})
		log := argvRecorder()
		lines, err := cache.Blame(ctx, repo, git.BlameOptions{SHA: created, Path: "rule.xml", Contents: []byte(original)})
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(HaveLen(3))
		Expect(lines[1].Commit.SHA).To(Equal(created))
		commands, err := os.ReadFile(log)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(commands)).NotTo(ContainSubstring("worktree"))
		_, err = os.Stat(filepath.Join(cache.Root, repo.URL.CacheKey(), "worktrees"))
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	It("attributes displayed lines across a rename and marks inserted and changed lines without writing them", func(ctx SpecContext) {
		const original = "<Rule>\n  <Value>old</Value>\n</Rule>\n"
		created := fixture.commitOn(fixtureCommit{Branch: "main", Path: "old.xml", Content: original, Message: "Add rule"})
		fixture.changeOn("main", "Rename rule", "mv", "old.xml", "[Plan] rule.xml")
		const displayed = "<Rule>\n  <Added/>\n  <Value>new</Value>\n</Rule>\n"
		head := gitFixture(fixture.work(), "rev-parse", "HEAD")
		lines, err := cache.Blame(ctx, repo, git.BlameOptions{SHA: head, Path: "[Plan] rule.xml", Contents: []byte(displayed)})
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(HaveLen(4))
		Expect([]any{lines[0].Line, lines[0].OriginalLine, lines[0].Path, lines[0].Content, lines[0].Commit.SHA, lines[0].Commit.Subject}).To(Equal(
			[]any{1, 1, "old.xml", "<Rule>", created, "Add rule"}))
		Expect([]any{lines[1].Commit, lines[2].Commit, lines[3].Commit.SHA, lines[3].OriginalLine}).To(Equal(
			[]any{(*git.BlameCommit)(nil), (*git.BlameCommit)(nil), created, 3}))
		body, err := cache.Show(ctx, repo, head, "[Plan] rule.xml")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(Equal(original))
	})

	DescribeTable("preserves displayed line boundaries", func(ctx SpecContext, displayed string, expected []string) {
		head := fixture.commitOn(fixtureCommit{Branch: "main", Path: "rule.xml", Content: "<Rule>\n  <Value/>\n</Rule>\n", Message: "Add rule"})
		lines, err := cache.Blame(ctx, repo, git.BlameOptions{SHA: head, Path: "rule.xml", Contents: []byte(displayed)})
		Expect(err).NotTo(HaveOccurred())
		contents := make([]string, len(lines))
		for i, line := range lines {
			contents[i] = line.Content
		}
		Expect(contents).To(Equal(expected))
	},
		Entry("empty document", "", []string{}),
		Entry("CRLF and blank lines", "<Rule>\r\n\r\n</Rule>\r\n", []string{"<Rule>\r", "\r", "</Rule>\r"}),
		Entry("no final newline", "<Rule>\n</Rule>", []string{"<Rule>", "</Rule>"}),
		Entry("long XML line", strings.Repeat("x", 100_000), []string{strings.Repeat("x", 100_000)}),
	)

	It("uses the requested commit after its branch advances and completes a shallow cache", func(ctx SpecContext) {
		const displayed = "<Rule>\n<Value/>\n</Rule>\n"
		created := fixture.commitOn(fixtureCommit{Branch: "main", Path: "rule.xml", Content: displayed, Message: "Add rule"})
		head := fixture.commitOn(fixtureCommit{Branch: "main", Path: "other.xml", Content: "<Other/>", Message: "Other rule"})
		wt, err := cache.Checkout(ctx, repo, git.CheckoutOptions{Ref: head, Depth: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(wt.Release()).To(Succeed())
		fixture.commitOn(fixtureCommit{Branch: "main", Path: "rule.xml", Content: "<Replaced/>\n", Message: "Replace rule"})
		lines, err := cache.Blame(ctx, repo, git.BlameOptions{SHA: head, Path: "rule.xml", Contents: []byte(displayed)})
		Expect(err).NotTo(HaveOccurred())
		Expect(lines[1].Commit.SHA).To(Equal(created))
	})

	It("retains each line's original author and commit", func(ctx SpecContext) {
		created := fixture.commitOn(fixtureCommit{Branch: "main", Path: "rule.xml", Content: "<Rule>\n<Old/>\n</Rule>\n", Message: "Add rule"})
		const displayed = "<Rule>\n<New/>\n</Rule>\n"
		writeFixtureFile(fixture.work(), "rule.xml", displayed)
		gitFixture(fixture.work(), "add", "rule.xml")
		gitFixture(fixture.work(), "commit", "--quiet", "--author=Grace Example <grace@example.test>", "-m", "Update rule")
		gitFixture(fixture.work(), "push", "--quiet", fixture.Path, "main")
		head := gitFixture(fixture.work(), "rev-parse", "HEAD")
		lines, err := cache.Blame(ctx, repo, git.BlameOptions{SHA: head, Path: "rule.xml", Contents: []byte(displayed)})
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{lines[0].Commit.SHA, lines[0].Commit.Author.Name, lines[1].Commit.SHA, lines[1].Commit.Author}).To(Equal(
			[]any{created, "Fixture", head, git.BlameAuthor{Name: "Grace Example", Email: "grace@example.test"}}))
	})

	It("rejects invalid SHA, escaping paths and missing contents", func(ctx SpecContext) {
		for _, options := range []git.BlameOptions{
			{SHA: "main", Path: "README.md", Contents: []byte("first")},
			{SHA: fixture.Main, Path: "../README.md", Contents: []byte("first")},
			{SHA: fixture.Main, Path: "README.md"},
		} {
			_, err := cache.Blame(ctx, repo, options)
			Expect(err).To(HaveOccurred())
		}
	})
})
