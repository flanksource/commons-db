package deadlocks_test

import (
	"context"
	"strings"

	"github.com/DATA-DOG/go-sqlmock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/tracing/deadlocks"
)

var _ = Describe("ApplyCatalog", func() {
	const labDatabaseID = 5
	pageLock := func(dbid int, hobt int64, index string) deadlocks.Graph {
		resolution := deadlocks.ResolutionUnresolved
		if index != "" {
			resolution = deadlocks.ResolutionNamed
		}
		return deadlocks.Graph{Deadlock: deadlocks.Deadlock{Resources: []deadlocks.Resource{{
			Kind: "pagelock", DatabaseID: dbid, HobtID: hobt, Index: index, Resolution: resolution,
		}}}}
	}
	catalog := []deadlocks.CatalogEntry{
		{HobtID: ix15Hobt, Object: "AsActivity", Index: "IX_15_ASACTIVITY", IndexType: "CLUSTERED"},
		{HobtID: ux1Hobt, Object: "AsActivity", Index: "UX_1_ASACTIVITY", IndexType: "NONCLUSTERED"},
	}
	type named struct {
		Object, Index string
		Type          deadlocks.IndexType
		Resolution    deadlocks.Resolution
	}
	DescribeTable("names each index lock from the connected database's catalog",
		func(graph deadlocks.Graph, want named) {
			graphs := []deadlocks.Graph{graph}
			deadlocks.ApplyCatalog(graphs, labDatabaseID, catalog)
			r := graphs[0].Resources[0]
			Expect(named{r.Object, r.Index, r.IndexType, r.Resolution}).To(Equal(want))
		},
		Entry("a page lock found in the catalog", pageLock(labDatabaseID, ix15Hobt, ""),
			named{"AsActivity", "IX_15_ASACTIVITY", deadlocks.IndexClustered, deadlocks.ResolutionResolved}),
		Entry("a key lock the report named keeps its name and gains its type", pageLock(labDatabaseID, ux1Hobt, "UX_1_ASACTIVITY"),
			named{"", "UX_1_ASACTIVITY", deadlocks.IndexNonclustered, deadlocks.ResolutionNamed}),
		Entry("a hobt a rebuild has since replaced", pageLock(labDatabaseID, 72057594000000000, ""),
			named{"", "", "", deadlocks.ResolutionRebuiltSince}),
		Entry("a lock in another database", pageLock(7, ix15Hobt, ""),
			named{"", "", "", deadlocks.ResolutionOtherDatabase}),
	)
})

var _ = Describe("Graph.Pretty", func() {
	It("reads the cycle as who waits for what, behind whom", func() {
		graph := decoded("key-lookup-two-process.xml")
		graph.Deadlock = withIndexTypes(graph, originalSchema)
		deadlocks.Analyze(&graph.Deadlock)

		text := graph.Pretty().String()
		Expect(text).To(ContainSubstring("2026-09-10 16:47:50.043 UTC · Key lookup vs writer · warehouse"))
		Expect(text).To(ContainSubstring("SPID 93 (victim) wants S on keylock AsActivity.IX_15_ASACTIVITY, held X by SPID 91"))
		Expect(text).To(ContainSubstring("SPID 91 wants X on keylock AsActivity.UX_1_ASACTIVITY, held S by SPID 93 (victim)"))
	})

	It("shows each participant's running statement, says when SQL Server cut it short, and dates the wait in UTC beside the server-time transaction start", func() {
		graph, err := deadlocks.DecodeReport(ix2ReportedAt, fixture("key-lookup-ix2-four-process.xml"))
		Expect(err).NotTo(HaveOccurred())
		graph.Deadlock = withIndexTypes(graph, reclusteredSchema)
		deadlocks.Analyze(&graph.Deadlock)

		text := graph.Pretty().Markdown()
		row := func(spid string) string {
			for _, line := range strings.Split(text, "\n") {
				if strings.Contains(line, "| "+spid+" ") {
					return line
				}
			}
			return ""
		}
		// The table clips wide cells, so each check reads a cell's start. Reader
		// SPID 211 keeps 987 of its 11,673-character statement: the 1,022
		// characters of trimmed buffer less the 35-character declaration. Writer
		// SPID 210's started at server time 15:46:20.377 (UTC+2) and it waited
		// 10,981 ms before 13:46:31.365 UTC.
		Expect([]bool{
			strings.Contains(text, "TRAN STARTED  ( SERVER TIME )"),
			strings.Contains(text, "WAITING SINCE  ( UTC )"),
			strings.Contains(row("211"), "| (truncated by SQL Server: 987 of 11,673 characters) SELECT AsActivity.ActivityGUID,"),
			strings.Contains(row("210"), "| UPDATE ASACTIVITY SET StatusCode = @P0 ,"),
			strings.Contains(row("210"), "truncated"),
			strings.Contains(row("210"), " 2026-09-14T15:46:20.377 "),
			strings.Contains(row("210"), " 2026-09-14 13:46:20.384 "),
		}).To(Equal([]bool{true, true, true, true, false, true, true}))
	})
})

var _ = Describe("Resolve", func() {
	It("looks up every hobt of the connected database with one bind marker each", func() {
		db, mock, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = db.Close() })

		lock := func(dbid int, hobt int64) deadlocks.Resource {
			return deadlocks.Resource{Kind: "pagelock", DatabaseID: dbid, HobtID: hobt, Resolution: deadlocks.ResolutionUnresolved}
		}
		graphs := []deadlocks.Graph{{Deadlock: deadlocks.Deadlock{Resources: []deadlocks.Resource{
			lock(5, ix15Hobt), lock(5, ux1Hobt), lock(5, ix15Hobt), lock(7, 42),
		}}}}
		mock.ExpectQuery(`SELECT DB_ID\(\)`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(5))
		mock.ExpectQuery(`WHERE p.hobt_id IN \(@p1, @p2\)`).WithArgs(ix15Hobt, ux1Hobt).WillReturnRows(
			sqlmock.NewRows([]string{"hobt_id", "object_name", "index_name", "type_desc"}).
				AddRow(ix15Hobt, "AsActivity", "IX_15_ASACTIVITY", "CLUSTERED").
				AddRow(ux1Hobt, nil, "UX_1_ASACTIVITY", "NONCLUSTERED"))

		Expect(deadlocks.Resolve(context.Background(), db, graphs)).To(Succeed())
		Expect(mock.ExpectationsWereMet()).To(Succeed())

		resources := graphs[0].Resources
		Expect(resources[0].Object).To(Equal("AsActivity"))
		Expect(resources[0].IndexType).To(Equal(deadlocks.IndexClustered))
		Expect(resources[1].Index).To(Equal("UX_1_ASACTIVITY"), "a dropped object's NULL name still resolves its index")
		Expect(resources[3].Resolution).To(Equal(deadlocks.ResolutionOtherDatabase))
	})
})
