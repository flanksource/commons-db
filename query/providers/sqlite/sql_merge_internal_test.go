package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// mergeFixtureRows is each partition's rows. ids repeat across partitions, the
// unique column only being unique within one file; val mixes NULL, integers on
// both sides of 2^53, reals and text so the merge must order storage classes the
// way SQLite does; ts ties across partitions.
func mergeFixtureRows(partition int) [][]any {
	big := int64(1) << 53
	vals := []any{nil, int64(3), 2.5, "b", int64(partition), big + int64(partition%2), "a", 3.0, nil, []byte{1}, int64(-1), 2.25}
	rows := make([][]any, 0, len(vals))
	for index, val := range vals {
		var ts any = int64((index * 7) % 5)
		if index%4 == 3 {
			ts = nil
		}
		rows = append(rows, []any{int64(index), ts, val, fmt.Sprintf("p%02d", partition)})
	}
	return rows
}

const mergeFixtureSchema = `CREATE TABLE events (id INTEGER NOT NULL, ts INTEGER, val, part TEXT NOT NULL)`

func openMergeFixture(path string, partitions []int) *sql.DB {
	database, err := sql.Open("sqlite", "file:"+path)
	Expect(err).ToNot(HaveOccurred())
	DeferCleanup(database.Close)
	_, err = database.Exec(mergeFixtureSchema)
	Expect(err).ToNot(HaveOccurred())
	for _, partition := range partitions {
		for _, row := range mergeFixtureRows(partition) {
			_, err = database.Exec(`INSERT INTO events (id, ts, val, part) VALUES (?, ?, ?, ?)`, row...)
			Expect(err).ToNot(HaveOccurred())
		}
	}
	return database
}

func mergeMembers(count int) []SQLMember {
	dir := GinkgoT().TempDir()
	members := make([]SQLMember, 0, count)
	for partition := range count {
		name := fmt.Sprintf("p%02d", partition)
		members = append(members, SQLMember{Name: name, Client: openMergeFixture(filepath.Join(dir, name+".sqlite"), []int{partition})})
	}
	return members
}

// unionRows is the oracle: every partition's rows in one file, read in the
// merge's order with the member name as the final tiebreak.
func unionRows(count int, order query.Order) []query.Row {
	partitions := make([]int, 0, count)
	for partition := range count {
		partitions = append(partitions, partition)
	}
	union := openMergeFixture(filepath.Join(GinkgoT().TempDir(), "union.sqlite"), partitions)
	statement := "SELECT id, ts, val, part FROM events ORDER BY "
	for _, by := range order {
		direction := "ASC"
		if by.Desc {
			direction = "DESC"
		}
		statement += fmt.Sprintf("%q %s NULLS LAST, ", by.Column, direction)
	}
	result, err := ReadSQLPage(context.Background(), union, models.ConnectionTypeSQLite, SQLPageRequest{
		Query: statement + "part ASC", Page: query.PageRequest{Limit: count * 100, SkipTotal: true},
	})
	Expect(err).ToNot(HaveOccurred())
	return result.Rows
}

func mergeRequest(members []SQLMember, order query.Order, limit int) SQLMergeRequest {
	return SQLMergeRequest{
		Driver: models.ConnectionTypeSQLite, Members: members,
		Read: SQLPageRequest{
			Query: "SELECT id, ts, val, part FROM events", Order: order,
			Page: query.PageRequest{Limit: limit, Strategy: query.PagingCursor},
		},
	}
}

// walkResumed reads one page per request, resuming each from the last page's
// keys and state the way the engine's cursor carries them.
func walkResumed(request SQLMergeRequest) []query.Row {
	var rows []query.Row
	for range 10_000 {
		var page query.Page
		for read, err := range MergeSQLPages(context.Background(), request) {
			Expect(err).ToNot(HaveOccurred())
			page = read
			break
		}
		rows = append(rows, page.Rows...)
		if !page.HasMore {
			return rows
		}
		Expect(page.NextKeys).To(HaveLen(len(request.Read.Order)))
		request.Read.Position = query.CursorPosition{Keys: page.NextKeys, State: page.State}
	}
	Fail("merged walk did not finish")
	return nil
}

func walkInline(request SQLMergeRequest) []query.Row {
	var rows []query.Row
	for page, err := range MergeSQLPages(context.Background(), request) {
		Expect(err).ToNot(HaveOccurred())
		rows = append(rows, page.Rows...)
	}
	return rows
}

var _ = Describe("MergeSQLPages", func() {
	byTimeDesc := query.Order{{Column: "ts", Desc: true}, {Column: "id", Unique: true}}
	byVal := query.Order{{Column: "val"}, {Column: "id", Unique: true}}
	byValDesc := query.Order{{Column: "val", Desc: true}, {Column: "id", Unique: true}}

	DescribeTable("merges partitions into the order one union file returns",
		func(partitions, limit int, order query.Order) {
			members := mergeMembers(partitions)
			expected := unionRows(partitions, order)
			Expect(walkResumed(mergeRequest(members, order, limit))).To(Equal(expected), "resumed page by page")
			Expect(walkInline(mergeRequest(members, order, limit))).To(Equal(expected), "walked in one range")
		},
		Entry("1 partition, page 7, ts desc", 1, 7, byTimeDesc),
		Entry("8 partitions, page 1, ts desc", 8, 1, byTimeDesc),
		Entry("8 partitions, page 7, ts desc", 8, 7, byTimeDesc),
		Entry("8 partitions, page 100, val asc", 8, 100, byVal),
		Entry("8 partitions, page 7, val desc", 8, 7, byValDesc),
		Entry("32 partitions, page 7, ts desc", 32, 7, byTimeDesc),
		Entry("32 partitions, page 100, val desc", 32, 100, byValDesc),
	)

	It("sums the members' totals on the first page", func() {
		const partitions = 3
		var first query.Page
		for page, err := range MergeSQLPages(context.Background(), mergeRequest(mergeMembers(partitions), byTimeDesc, 5)) {
			Expect(err).ToNot(HaveOccurred())
			first = page
			break
		}
		Expect(first.Total).To(Equal(&query.Total{Value: int64(partitions * len(mergeFixtureRows(0))), Exact: true}))
	})

	It("caps a walk at the page ceiling and reports it truncated", func() {
		request := mergeRequest(mergeMembers(4), byTimeDesc, 5)
		request.Read.Page.Ceiling = 12
		var pages []query.Page
		for page, err := range MergeSQLPages(context.Background(), request) {
			Expect(err).ToNot(HaveOccurred())
			pages = append(pages, page)
		}
		Expect(pages).To(HaveLen(3))
		Expect(pages[2].Rows).To(HaveLen(2))
		Expect(pages[2].Truncated).To(BeTrue())
		Expect(pages[2].HasMore).To(BeFalse())
	})

	DescribeTable("refuses a read it cannot serve",
		func(mutate func(*SQLMergeRequest), message string) {
			request := mergeRequest(mergeMembers(2), byTimeDesc, 5)
			mutate(&request)
			for _, err := range MergeSQLPages(context.Background(), request) {
				Expect(err).To(MatchError(ContainSubstring(message)))
				return
			}
			Fail("expected an error page")
		},
		Entry("an offset", func(r *SQLMergeRequest) { r.Read.Page.Offset = 5 }, "cursor only"),
		Entry("an order without a unique tiebreaker", func(r *SQLMergeRequest) {
			r.Read.Order = query.Order{{Column: "ts"}}
		}, "unique"),
		Entry("a cursor without per-partition positions", func(r *SQLMergeRequest) {
			r.Read.Position = query.CursorPosition{Keys: []any{int64(1), int64(2)}}
		}, "not issued by a merged read"),
		Entry("a cursor from a different partition set", func(r *SQLMergeRequest) {
			r.Read.Position = query.CursorPosition{
				Keys:  []any{int64(1), int64(2)},
				State: map[string][]byte{query.ProviderStateKey: []byte(`{"p00":{},"p07":{}}`)},
			}
		}, `member "p01" was not part of the read`),
		Entry("duplicate member names", func(r *SQLMergeRequest) { r.Members[1].Name = r.Members[0].Name }, "unique, non-empty names"),
	)
})

var _ = Describe("CompareSQLite", func() {
	big := int64(1) << 53
	DescribeTable("orders values as SQLite's BINARY collation does",
		func(a, b any, expected int) {
			Expect(CompareSQLite(a, b)).To(Equal(expected))
			Expect(CompareSQLite(b, a)).To(Equal(-expected))
		},
		Entry("null before a number", nil, int64(0), -1),
		Entry("number before text", 1e300, "0", -1),
		Entry("text before a blob", "zzz", []byte{0}, -1),
		Entry("int64 above 2^53 exactly", big, big+1, -1),
		Entry("int against a real fraction", int64(2), 2.5, -1),
		Entry("int equal to a whole real", int64(3), 3.0, 0),
		Entry("negative int against a real", int64(-3), -2.5, -1),
		Entry("int against a real beyond int64", int64(1)<<62, 1e19, -1),
		Entry("bool as an integer", true, int64(1), 0),
		Entry("text bytewise", "B", "a", -1),
		Entry("blobs bytewise", []byte{1, 2}, []byte{1, 3}, -1),
	)

	It("refuses a value SQLite cannot hold", func() {
		_, err := CompareSQLite(struct{}{}, int64(1))
		Expect(err).To(MatchError(ContainSubstring("cannot order")))
	})
})
