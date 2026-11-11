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
	It("renders phase counters natively, retains labels, and clears an earlier phase total", func() {
		group := task.StartGroup[bool]("native progress", task.WithConcurrency(1))
		observe := Tasks(TaskOptions{Group: func(context.Context) *task.Group { return group.Group }})
		event := git.FetchProgress{ID: "phase", Repository: "https://dev.azure.com/acme/tenant-x/_git/rules", Ref: "main", Phase: "Receiving objects", Current: 5, Total: 10, Bytes: 1024, BytesPerSecond: 512}
		observe(group.Context(), event)
		snap := task.SnapshotTask(group.GetTasks()[0].GetTask(), group.Group)
		Expect(snap.Description).To(Equal("Receiving objects"))
		Expect(snap.Progress).To(Equal(5))
		Expect(snap.Labels["repository"]).To(Equal("tenant-x/rules"))
		Expect(snap.ProgressMetrics.Bytes).To(Equal(int64(1024)))
		event.Phase, event.Current, event.Total, event.PhaseDone = "Enumerating objects", 2, 0, true
		observe(group.Context(), event)
		snap = task.SnapshotTask(group.GetTasks()[0].GetTask(), group.Group)
		Expect(snap.MaxValue).To(BeZero())
		Expect(snap.Logs).To(BeEmpty())
		event.Done = true
		observe(group.Context(), event)
	})
	DescribeTable("reads like git's own meter", func(progress git.FetchProgress, expected string) {
		Expect(fetchProgressLine(progress)).To(Equal(expected))
	},
		Entry("server phase", git.FetchProgress{Phase: "Counting objects", Remote: true, Percent: 50, Current: 5, Total: 10},
			"Counting objects"),
		Entry("transfer with throughput", git.FetchProgress{Phase: "Receiving objects", Percent: 45, Current: 450, Total: 1000, Transferred: "12.00 MiB", Rate: "3.00 MiB/s"},
			"Receiving objects"),
		Entry("finished phase", git.FetchProgress{Phase: "Resolving deltas", Percent: 100, Current: 3, Total: 3, PhaseDone: true},
			"Resolving deltas"),
		Entry("unbounded count", git.FetchProgress{Phase: "Enumerating objects", Remote: true, Current: 12, PhaseDone: true},
			"Enumerating objects"),
	)

	DescribeTable("names the fetch by its repository's last path segments and ref", func(repository, ref, expected string) {
		Expect(fetchTaskName(git.FetchProgress{Repository: repository, Ref: ref})).To(Equal(expected))
	},
		Entry("hosted repository and branch", "https://example.org/acme/rules.git", "main", "[acme/rules] Fetch main"),
		Entry("local repository and commit", "file:///work/acme/rules", "49ec96f75b56e8080a84a4eaf3ae345476f04908", "[acme/rules] Fetch @49ec96f75b56"),
		Entry("single-segment path", "file:///rules.git", "v1", "[rules] Fetch v1"),
		Entry("azure project", "https://dev.azure.com/acme/tenant-x/_git/rules", "main", "[tenant-x/rules] Fetch main"),
		Entry("nested gitlab", "https://gitlab.com/acme/team/rules.git", "main", "[acme/team/rules] Fetch main"),
	)

	DescribeTable("names the other repository's mirror a shared fetch writes into", func(progress git.FetchProgress, expected string) {
		progress.Repository, progress.Ref = "https://dev.azure.com/acme/tenant-x/_git/rules-bot", "49ec96f75b56e8080a84a4eaf3ae345476f04908"
		Expect(fetchTaskName(progress)).To(Equal(expected))
	},
		Entry("own mirror", git.FetchProgress{}, "[tenant-x/rules-bot] Fetch @49ec96f75b56"),
		Entry("recorded owner", git.FetchProgress{Mirror: "dev.azure.com_acme_tenant-x__git_rules-mal-0123456789ab", MirrorRepository: "https://dev.azure.com/acme/tenant-x/_git/rules-mal"},
			"[tenant-x/rules-bot] Fetch @49ec96f75b56 → tenant-x/rules-mal"),
		Entry("unrecorded owner", git.FetchProgress{Mirror: "dev.azure.com_acme_tenant-x__git_rules-mal-0123456789ab"},
			"[tenant-x/rules-bot] Fetch @49ec96f75b56 → dev.azure.com_acme_tenant-x__git_rules-mal-0123456789ab"),
	)

	It("labels a shared fetch with the mirror it writes into", func() {
		group := task.StartGroup[bool]("shared mirror", task.WithConcurrency(1))
		observe := Tasks(TaskOptions{Group: func(context.Context) *task.Group { return group.Group }})
		event := git.FetchProgress{ID: "shared", Repository: "https://dev.azure.com/acme/tenant-x/_git/rules-bot", Ref: "main",
			Mirror: "dev.azure.com_acme_tenant-x__git_rules-mal-0123456789ab", Phase: "Receiving objects"}
		observe(group.Context(), event)
		snap := task.SnapshotTask(group.GetTasks()[0].GetTask(), group.Group)
		Expect(snap.Labels).To(Equal(map[string]string{"repository": "tenant-x/rules-bot", "ref": "main", "operation": "fetch",
			"mirror": "dev.azure.com_acme_tenant-x__git_rules-mal-0123456789ab"}))
		event.Done = true
		observe(group.Context(), event)
	})

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
