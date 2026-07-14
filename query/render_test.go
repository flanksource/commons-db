package query_test

import (
	"strings"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CEL columns", func() {
	It("computes a column value from the row", func() {
		query.RegisterProvider(&mockProvider{
			typ:  "cel-source",
			rows: []query.Row{{"duration_ms": 1500.0}, {"duration_ms": 500.0}},
		})

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "cel",
			Provider: query.ProviderConfig{Type: "cel-source"},
			Columns: []query.ColumnDef{
				{Name: "seconds", Type: query.ColumnTypeNumber, CEL: "row.duration_ms / 1000.0"},
			},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows[0]).To(HaveKeyWithValue("seconds", 1.5))
		Expect(result.Rows[1]).To(HaveKeyWithValue("seconds", 0.5))
	})

	It("fails loudly on an invalid CEL expression", func() {
		query.RegisterProvider(&mockProvider{typ: "cel-bad", rows: []query.Row{{"a": 1.0}}})

		_, err := query.Execute(context.New(), query.Profile{
			Name:     "cel-bad",
			Provider: query.ProviderConfig{Type: "cel-bad"},
			Columns:  []query.ColumnDef{{Name: "x", CEL: "row.a +"}},
		})
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("Result.Render", func() {
	result := &query.Result{Rows: []query.Row{
		{"id": 1, "name": "alpha"},
		{"id": 2, "name": "beta"},
	}}
	columns := []query.ColumnDef{{Name: "id"}, {Name: "name"}}

	It("renders CSV with a header row and one line per row", func() {
		out, err := result.Render(columns, "csv")
		Expect(err).ToNot(HaveOccurred())

		lines := strings.Split(strings.TrimSpace(out), "\n")
		// clicky prettifies headers: id -> Id, name -> Name.
		Expect(lines[0]).To(And(ContainSubstring("Id"), ContainSubstring("Name")))
		Expect(out).To(ContainSubstring("alpha"))
		Expect(out).To(ContainSubstring("beta"))
	})

	It("renders JSON containing the row values", func() {
		out, err := result.Render(columns, "json")
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(ContainSubstring("alpha"))
		Expect(out).To(ContainSubstring("beta"))
	})

	It("renders header chrome even when there are no rows", func() {
		empty := &query.Result{Rows: nil}
		out, err := empty.Render(columns, "csv")
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(ContainSubstring("Name"))
	})

	It("renders a self-contained HTML report (clicky-ui / ClickyDocument contract)", func() {
		out, err := result.Render(columns, "html")
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(ContainSubstring("<"))
		Expect(out).To(ContainSubstring("alpha"))
	})

	It("preserves timestamp column behavior in clicky JSON", func() {
		out, err := result.Render([]query.ColumnDef{{Name: "name", Kind: query.ColumnKindTimestamp}}, "clicky-json")
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(ContainSubstring(`"kind": "timestamp"`))
	})

	It("preserves structured column types and nodes in clicky JSON", func() {
		structured := &query.Result{Rows: []query.Row{{
			"labels":   map[string]any{"env": "prod"},
			"metadata": map[string]any{"enabled": true},
		}}}
		out, err := structured.Render([]query.ColumnDef{
			{Name: "labels", Type: query.ColumnTypeKeyValue},
			{Name: "metadata", Type: query.ColumnTypeJSON},
		}, "clicky-json")
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(ContainSubstring(`"type": "key_value"`))
		Expect(out).To(ContainSubstring(`"kind": "map"`))
		Expect(out).To(ContainSubstring(`"language": "json"`))
	})
})
