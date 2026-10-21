package git_test

import (
	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Git file discovery", func() {
	It("lists files at an immutable commit, including nested names and ignored tracked files", func(ctx SpecContext) {
		fixture := newBareRepo("rules")
		first := fixture.commitOn("main", "", "reports/security groups.xlsx", "workbook", "add workbook")
		fixture.commitOn("main", "", "later.xlsx", "later", "add later workbook")
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		repo := newRepository(fixture.FileURL(), git.Auth{})
		files, err := cache.Files(ctx, repo, first)
		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(ContainElement("reports/security groups.xlsx"))
		Expect(files).NotTo(ContainElement("later.xlsx"))
		_, err = cache.Files(ctx, repo, "main")
		Expect(err).To(HaveOccurred())
	})
	It("paginates history without changing the pinned revision", func(ctx SpecContext) {
		fixture := newBareRepo("rules")
		latest := fixture.commitOn("main", "", "second.txt", "second", "second")
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		repo := newRepository(fixture.FileURL(), git.Auth{})
		page, err := cache.History(ctx, repo, latest, git.HistoryOptions{Limit: 1, Offset: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(page).To(HaveLen(1))
		Expect(page[0].SHA).To(Equal(fixture.Main))
	})
})
