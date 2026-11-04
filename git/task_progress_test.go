package git_test

import (
	"github.com/flanksource/clicky/task"
	"github.com/flanksource/commons-db/git"
	gitprogress "github.com/flanksource/commons-db/git/progress"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Repository task progress", func() {
	It("reports cold and warm file reads without exposing executable version checks", func(ctx SpecContext) {
		fixture := newBareRepo("task-progress")
		repo := newRepository(fixture.FileURL(), git.Auth{})
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		group := task.StartGroup[bool]("comparison", task.WithConcurrency(1))
		active := gitprogress.WithTasks(group.Context(), gitprogress.TaskOptions{})
		for range 2 {
			files, err := cache.Files(active, repo, fixture.Main)
			Expect(err).NotTo(HaveOccurred())
			Expect(files).NotTo(BeEmpty())
		}
		Expect(group.GetTasks()).To(HaveLen(2))
		for _, item := range group.GetTasks() {
			snapshot := task.SnapshotTask(item.GetTask(), group.Group)
			Expect(snapshot.Name).To(HavePrefix("[" + repo.URL.DisplayName() + "] Files"))
			Expect(snapshot.Labels["repository"]).To(Equal(repo.URL.DisplayName()))
		}
		Expect(group.WaitFor().Error).NotTo(HaveOccurred())
	})
})
