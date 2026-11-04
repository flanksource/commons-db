package progress

import (
	"context"

	"github.com/flanksource/clicky/task"
	"github.com/flanksource/commons-db/git/readcache"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("nested cache progress", func() {
	It("names the read and attaches it to the active child rather than the supplied ancestor", func() {
		parent := task.StartGroup[bool]("PR")
		child := task.StartGroup[bool]("left side", task.WithParentGroup(parent.Group))
		observer := CacheTasks(TaskOptions{Group: func(context.Context) *task.Group { return parent.Group }})
		event := readcache.Event{ID: "read", Metadata: readcache.Metadata{Operation: "blame", Repository: "example/repo", Ref: "main"}, Disposition: "lookup"}
		observer(child.Context(), event)
		Expect(parent.GetTasks()).To(BeEmpty())
		Expect(child.GetTasks()).To(HaveLen(1))
		Expect(child.GetTasks()[0].GetTask().Name()).To(Equal("[example/repo] Blame main"))
		event.Done, event.Disposition = true, "hit"
		observer(child.Context(), event)
		Expect(parent.WaitFor().Status).To(Equal(task.StatusSuccess))
	})
})
