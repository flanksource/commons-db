package sqlite

import (
	"database/sql"
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const dayLabels = `dt=(?P<dt>[0-9-]+)`

var partitionDays = []string{"2026-10-29", "2026-10-30", "2026-10-31"}

// writeDayPartitions writes one events file per day under dir/dt=<day>/, each
// holding ids 1..rowsPerDay so the unique column repeats across files.
func writeDayPartitions(dir string, rowsPerDay int) {
	for _, day := range partitionDays {
		path := filepath.Join(dir, "dt="+day, "events.sqlite")
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		database, err := sql.Open("sqlite", "file:"+path)
		Expect(err).ToNot(HaveOccurred())
		_, err = database.Exec(`CREATE TABLE events (id INTEGER PRIMARY KEY, service TEXT NOT NULL)`)
		Expect(err).ToNot(HaveOccurred())
		for id := 1; id <= rowsPerDay; id++ {
			_, err = database.Exec(`INSERT INTO events (id, service) VALUES (?, ?)`, id, []string{"api", "web"}[id%2])
			Expect(err).ToNot(HaveOccurred())
		}
		Expect(database.Close()).To(Succeed())
	}
}

func dayFilter(include, exclude []string) query.ColumnFilterValue {
	return query.ColumnFilterValue{Column: "dt", Field: "dt", Include: include, Exclude: exclude}
}

var _ = Describe("PartitionSet.Resolve", func() {
	var dir string
	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		writeDayPartitions(dir, 1)
	})
	glob := func() string { return filepath.Join(dir, "dt=*", "events.sqlite") }
	days := func(partitions []Partition) []string {
		names := make([]string, 0, len(partitions))
		for _, partition := range partitions {
			names = append(names, partition.Labels["dt"])
		}
		return names
	}

	It("lists globbed files in path order with their labels and read-only DSNs", func() {
		partitions, err := PartitionSet{Glob: glob(), Labels: dayLabels, Immutable: true}.Resolve(nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(days(partitions)).To(Equal(partitionDays))
		Expect(partitions[0].Name).To(Equal(filepath.Join(dir, "dt=2026-10-29", "events.sqlite")))
		Expect(partitions[0].DSN).To(And(ContainSubstring("mode=ro"), HaveSuffix("&immutable=1")))
	})

	DescribeTable("skips partitions an exact-value filter on a label excludes",
		func(filter query.ColumnFilterValue, expected []string) {
			partitions, err := PartitionSet{Glob: glob(), Labels: dayLabels}.Resolve([]query.ColumnFilterValue{filter})
			Expect(err).ToNot(HaveOccurred())
			Expect(days(partitions)).To(Equal(expected))
		},
		Entry("an include", dayFilter([]string{"2026-10-30"}, nil), []string{"2026-10-30"}),
		Entry("an exclude", dayFilter(nil, []string{"2026-10-30"}), []string{"2026-10-29", "2026-10-31"}),
		Entry("a substring filter, left to SQL", query.ColumnFilterValue{
			Field: "dt", Kind: query.ColumnFilterKindText, Include: []string{"10-30"},
		}, partitionDays),
		Entry("a filter on a stored column, left to SQL", query.ColumnFilterValue{
			Field: "service", Include: []string{"api"},
		}, partitionDays),
	)

	DescribeTable("refuses a partition set that names no usable files",
		func(set func() PartitionSet, filters []query.ColumnFilterValue, message string) {
			_, err := set().Resolve(filters)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("neither files nor glob", func() PartitionSet { return PartitionSet{} }, nil, "files or glob is required"),
		Entry("both files and glob", func() PartitionSet {
			return PartitionSet{Files: []string{"a.sqlite"}, Glob: glob()}
		}, nil, "exclusive"),
		Entry("a glob matching nothing", func() PartitionSet {
			return PartitionSet{Glob: filepath.Join(dir, "missing", "*.sqlite")}
		}, nil, "matches no files"),
		Entry("a path outside the label layout", func() PartitionSet {
			return PartitionSet{Glob: glob(), Labels: `day=(?P<day>[0-9-]+)`}
		}, nil, "does not match labels"),
		Entry("labels with no named group", func() PartitionSet {
			return PartitionSet{Glob: glob(), Labels: `dt=[0-9-]+`}
		}, nil, "no named group"),
		Entry("filters excluding every partition", func() PartitionSet {
			return PartitionSet{Glob: glob(), Labels: dayLabels}
		}, []query.ColumnFilterValue{dayFilter([]string{"2026-11-01"}, nil)}, "no files remain of 3"),
	)
})

var _ = Describe("sqlite provider over partitions", func() {
	const rowsPerDay = 5
	var dir string
	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		writeDayPartitions(dir, rowsPerDay)
	})
	profile := func() query.Profile {
		return query.Profile{
			Name:     "events-by-day",
			Provider: query.ProviderConfig{Type: "sqlite", Options: map[string]any{"partitions": map[string]any{
				"glob": filepath.Join(dir, "dt=*", "events.sqlite"), "labels": dayLabels,
			}}},
			Query: "SELECT id, service FROM events",
			Order: query.Order{{Column: "dt", Desc: true}, {Column: "id", Unique: true}},
		}
	}

	It("walks every partition by cursor in the merged order, labels included", func() {
		var rows []query.Row
		request := query.PageRequest{Limit: 4, Strategy: query.PagingCursor}
		for range 20 {
			var page query.Page
			for read, err := range query.ExecutePages(context.New(), profile(), request) {
				Expect(err).ToNot(HaveOccurred())
				page = read
				break
			}
			rows = append(rows, page.Rows...)
			if page.Next.IsZero() {
				break
			}
			request.Cursor = page.Next
		}
		Expect(rows).To(HaveLen(rowsPerDay * len(partitionDays)))
		Expect(rows[0]).To(Equal(query.Row{"id": int64(1), "service": "web", "dt": "2026-10-31"}))
		Expect(rows[rowsPerDay]).To(HaveKeyWithValue("dt", "2026-10-30"))
		Expect(rows[len(rows)-1]).To(Equal(query.Row{"id": int64(rowsPerDay), "service": "web", "dt": "2026-10-29"}))
	})

	It("filters by a label column in SQL and by pruning alike", func() {
		profile := profile()
		profile.Order = query.Order{{Column: "id", Unique: true}}
		provider, err := query.GetProvider("sqlite")
		Expect(err).ToNot(HaveOccurred())
		rows, err := provider.Execute(context.New(), query.ProviderRequest{
			Query: profile.Query, Options: profile.Provider.Options, Order: profile.Order,
			Filters: []query.ColumnFilterValue{dayFilter([]string{"2026-10-30"}, nil), {Field: "service", Include: []string{"api"}}},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(Equal([]query.Row{
			{"id": int64(2), "service": "api", "dt": "2026-10-30"},
			{"id": int64(4), "service": "api", "dt": "2026-10-30"},
		}))
	})

	It("refuses partitions alongside a connection url", func() {
		provider, err := query.GetProvider("sqlite")
		Expect(err).ToNot(HaveOccurred())
		options := profile().Provider.Options
		options["url"] = "file:" + filepath.Join(dir, "other.sqlite")
		_, err = provider.Execute(context.New(), query.ProviderRequest{Query: "SELECT 1", Options: options})
		Expect(err).To(MatchError(ContainSubstring("a connection or url cannot be set as well")))
	})
})
