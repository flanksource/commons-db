package progress

import (
	"context"
	"errors"
	"testing"

	"github.com/flanksource/clicky/task"
	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestProgress(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Git task progress")
}

var _ = Describe("fetch progress line", func() {
	DescribeTable("reads like git's own meter", func(progress git.FetchProgress, expected string) {
		Expect(fetchProgressLine(progress)).To(Equal(expected))
	},
		Entry("server phase", git.FetchProgress{Phase: "Counting objects", Remote: true, Percent: 50, Current: 5, Total: 10},
			"remote: Counting objects  50% (5/10)"),
		Entry("transfer with throughput", git.FetchProgress{Phase: "Receiving objects", Percent: 45, Current: 450, Total: 1000, Transferred: "12.00 MiB", Rate: "3.00 MiB/s"},
			"Receiving objects  45% (450/1000), 12.00 MiB | 3.00 MiB/s"),
		Entry("finished phase", git.FetchProgress{Phase: "Resolving deltas", Percent: 100, Current: 3, Total: 3, PhaseDone: true},
			"Resolving deltas 100% (3/3), done"),
		Entry("unbounded count", git.FetchProgress{Phase: "Enumerating objects", Remote: true, Current: 12, PhaseDone: true},
			"remote: Enumerating objects 12, done"),
	)

	DescribeTable("names the fetch by its repository's last path segments and ref", func(repository, ref, expected string) {
		Expect(fetchTaskName(git.FetchProgress{Repository: repository, Ref: ref})).To(Equal(expected))
	},
		Entry("hosted repository and branch", "https://example.org/acme/rules.git", "main", "git fetch acme/rules#main"),
		Entry("local repository and commit", "file:///work/acme/rules", "49ec96f75b56e8080a84a4eaf3ae345476f04908", "git fetch acme/rules@49ec96f75b56"),
		Entry("single-segment path", "file:///rules.git", "v1", "git fetch rules#v1"),
	)

	It("keeps simultaneous identical fetches distinct and finishes inline at concurrency one", func() {
		parent := task.StartGroup[bool]("git owner", task.WithConcurrency(1))
		group := task.StartGroup[bool]("git reads", task.WithParentGroup(parent.Group))
		observer := Tasks(TaskOptions{Group: func(context.Context) *task.Group { return group.Group }})
		first := git.FetchProgress{ID: "first", Repository: "file:///repo-a", Ref: "main"}
		second := git.FetchProgress{ID: "second", Repository: "file:///repo-a", Ref: "main"}
		failure := errors.New("fetch failed")
		for _, event := range []git.FetchProgress{
			withPhase(first, "Counting objects"), withPhase(second, "Counting objects"),
			withDone(first, nil), withDone(second, failure),
		} {
			observer(context.Background(), event)
		}
		Expect(group.GetTasks()).To(HaveLen(2))
		Expect(parent.WaitFor().Status).To(Equal(task.StatusFailed))
		Expect(task.SnapshotGroup(parent.Group).Failed).To(Equal(1))
	})
})

func withPhase(progress git.FetchProgress, phase string) git.FetchProgress {
	progress.Phase = phase
	return progress
}

func withDone(progress git.FetchProgress, err error) git.FetchProgress {
	progress.Done, progress.Err = true, err
	return progress
}
