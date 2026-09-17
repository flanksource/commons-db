package sessions

import (
	"github.com/flanksource/clicky/api"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
)

var _ = ginkgo.Describe("session table presentation", func() {
	ginkgo.DescribeTable("gives every state its labelled semantic pill",
		func(state query.SessionState, background, text string) {
			row := sessionRow(query.SessionInfo{SessionRecord: query.SessionRecord{
				SessionStart:  query.SessionStart{ID: "session-1", Labels: map[string]string{}},
				SessionStatus: query.SessionStatus{State: state},
			}}).Row()
			cell, ok := row["state"].(api.TableCell)
			Expect(ok).To(BeTrue())
			badge, ok := cell.Value.(api.LabelBadge)
			Expect(ok).To(BeTrue())
			Expect(badge).To(And(
				HaveField("Value", string(state)),
				HaveField("Color", background),
				HaveField("TextColor", text),
				HaveField("Shape", "pill"),
			))
		},
		ginkgo.Entry("starting", query.SessionStarting, "bg-sky-500/10", "text-sky-700"),
		ginkgo.Entry("running", query.SessionRunning, "bg-sky-500/10", "text-sky-700"),
		ginkgo.Entry("stopping", query.SessionStopping, "bg-yellow-500/10", "text-yellow-700"),
		ginkgo.Entry("completed", query.SessionCompleted, "bg-emerald-500/10", "text-emerald-700"),
		ginkgo.Entry("stopped", query.SessionStopped, "bg-slate-500/10", "text-slate-700"),
		ginkgo.Entry("failed", query.SessionFailed, "bg-red-500/10", "text-red-700"),
		ginkgo.Entry("interrupted", query.SessionInterrupted, "bg-yellow-500/10", "text-yellow-700"),
	)
})
