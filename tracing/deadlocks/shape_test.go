package deadlocks_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/tracing/deadlocks"
)

// withIndexTypes stamps the index types Resolve would have read from the
// catalog, which a spec without a database has to supply itself.
func withIndexTypes(graph deadlocks.Graph, types map[string]deadlocks.IndexType) deadlocks.Deadlock {
	for i := range graph.Resources {
		graph.Resources[i].IndexType = types[graph.Resources[i].Index]
	}
	return graph.Deadlock
}

// The lab's original AsActivity schema: clustered on IX_15, with UX_1 a
// nonclustered unique index on the activity GUID.
var originalSchema = map[string]deadlocks.IndexType{
	"IX_15_ASACTIVITY": deadlocks.IndexClustered,
	"UX_1_ASACTIVITY":  deadlocks.IndexNonclustered,
}

func decoded(name string) deadlocks.Graph {
	GinkgoHelper()
	graph, err := deadlocks.DecodeReport(reportedAt, fixture(name))
	Expect(err).NotTo(HaveOccurred())
	return graph
}

// scanVsWriter is the page-lock cycle the benchmark recorded on the original
// clustered index (docs/diagnose/sql-server.md, "a scan against a writer"):
// the reader holds S on one page and wants IX on another, the status UPDATE
// holds IX and wants S. Its lock rows survive only as the benchmark's
// aggregates (.tmp/deadlock-pagelock-indexes.json), so the graph is spelled
// out here with those modes and that hobt.
func scanVsWriter(indexType deadlocks.IndexType) deadlocks.Deadlock {
	page := func(lockID string, pageID int64, owner, ownerMode, waiter, waiterMode string) deadlocks.Resource {
		return deadlocks.Resource{
			Kind: "pagelock", LockID: lockID, DatabaseID: 5, Object: "AsActivity", Index: "IX_15_ASACTIVITY",
			IndexType: indexType, HobtID: ix15Hobt, FileID: 1, PageID: pageID, Resolution: deadlocks.ResolutionResolved,
			Owners:  []deadlocks.LockRequest{{ProcessID: owner, Mode: ownerMode}},
			Waiters: []deadlocks.LockRequest{{ProcessID: waiter, Mode: waiterMode, RequestType: "wait"}},
		}
	}
	return deadlocks.Deadlock{Resources: []deadlocks.Resource{
		page("lock1", 4410, "reader", "S", "writer", "IX"),
		page("lock2", 4411, "writer", "IX", "reader", "S"),
	}}
}

var _ = Describe("Analyze", func() {
	It("derives the objects, indexes, lock types and a process-independent signature", func() {
		deadlock := withIndexTypes(decoded("key-lookup-two-process.xml"), originalSchema)
		deadlocks.Analyze(&deadlock)

		Expect([]any{deadlock.Objects, deadlock.Indexes, deadlock.LockTypes, deadlock.Signature, deadlock.Shape}).To(Equal([]any{
			[]string{"AsActivity"},
			[]string{"IX_15_ASACTIVITY", "UX_1_ASACTIVITY"},
			[]string{"keylock"},
			"keylock AsActivity.IX_15_ASACTIVITY X→S ⇄ keylock AsActivity.UX_1_ASACTIVITY S→X",
			deadlocks.ShapeKeyLookupVsWriter,
		}))
	})

	It("gives a seven-victim pile-up the same signature as the two-process cycle it repeats", func() {
		two := withIndexTypes(decoded("key-lookup-two-process.xml"), originalSchema)
		seven := withIndexTypes(decoded("key-lookup-seven-victims.xml"), originalSchema)
		deadlocks.Analyze(&two)
		deadlocks.Analyze(&seven)

		Expect(seven.Signature).To(Equal(two.Signature),
			"queued owners (requestType=wait) are not held modes, so they do not change the signature")
		Expect(seven.Shape).To(Equal(deadlocks.ShapeKeyLookupVsWriter))
	})

	DescribeTable("classifies the cycle",
		func(build func() deadlocks.Deadlock, want deadlocks.Shape) {
			deadlock := build()
			deadlocks.Analyze(&deadlock)
			Expect(deadlock.Shape).To(Equal(want))
		},
		Entry("a key lookup against a writer on the original schema",
			func() deadlocks.Deadlock {
				return withIndexTypes(decoded("key-lookup-three-victims.xml"), originalSchema)
			}, deadlocks.ShapeKeyLookupVsWriter),
		Entry("the same key locks when UX_1 is the clustered index — no longer a lookup into the clustered row",
			func() deadlocks.Deadlock {
				return withIndexTypes(decoded("key-lookup-two-process.xml"), map[string]deadlocks.IndexType{
					"IX_15_ASACTIVITY": deadlocks.IndexNonclustered, "UX_1_ASACTIVITY": deadlocks.IndexClustered,
				})
			}, deadlocks.ShapeOther),
		Entry("key locks whose index types could not be resolved",
			func() deadlocks.Deadlock { return decoded("key-lookup-two-process.xml").Deadlock }, deadlocks.ShapeOther),
		Entry("a scan against a writer: crossed S and IX page locks on the clustered index",
			func() deadlocks.Deadlock { return scanVsWriter(deadlocks.IndexClustered) }, deadlocks.ShapeScanVsWriter),
		Entry("the same page locks on a nonclustered index",
			func() deadlocks.Deadlock { return scanVsWriter(deadlocks.IndexNonclustered) }, deadlocks.ShapeOther),
	)
})

var _ = Describe("Shape", func() {
	DescribeTable("labels each shape for a reader",
		func(shape deadlocks.Shape, label string) { Expect(shape.Label()).To(Equal(label)) },
		Entry(nil, deadlocks.ShapeScanVsWriter, "Scan vs writer"),
		Entry(nil, deadlocks.ShapeKeyLookupVsWriter, "Key lookup vs writer"),
		Entry(nil, deadlocks.ShapeOther, "Other"),
	)
})
