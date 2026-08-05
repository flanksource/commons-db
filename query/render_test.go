package query_test

import (
	"strings"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CEL columns", func() {
	It("renames a provider field and removes its original key", func() {
		query.RegisterProvider(&mockProvider{
			typ:  "renamed-source",
			rows: []query.Row{{"request_count": 12.0, "service": "payments"}},
		})

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "renamed",
			Provider: query.ProviderConfig{Type: "renamed-source"},
			Columns: []query.ColumnDef{
				{Name: "requests", Source: "request_count", Type: query.ColumnTypeNumber},
				{Name: "service"},
			},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(Equal([]query.Row{{"requests": 12.0, "service": "payments"}}))
	})

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

	It("extracts promoted fields from encoded and native JSON objects", func() {
		query.RegisterProvider(&mockProvider{typ: "cel-json-object", rows: []query.Row{
			{"metadata": `{"user":{"email":"alice@example.com"}}`},
			{"metadata": map[string]any{"user": map[string]any{"email": "bob@example.com"}}},
			{"message": "metadata omitted"},
		}})

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "cel-json-object",
			Provider: query.ProviderConfig{Type: "cel-json-object"},
			Columns: []query.ColumnDef{{
				Name: "user.email",
				CEL:  `'metadata' in row ? jsonpath("$['user']['email']", type(row['metadata']) == string ? row['metadata'].JSON() : row['metadata']) : ''`,
			}},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows[0]).To(HaveKeyWithValue("user.email", "alice@example.com"))
		Expect(result.Rows[1]).To(HaveKeyWithValue("user.email", "bob@example.com"))
		Expect(result.Rows[2]).To(HaveKeyWithValue("user.email", ""))
	})

	It("extracts promoted fields from encoded and native key-value arrays", func() {
		query.RegisterProvider(&mockProvider{typ: "cel-json-key-values", rows: []query.Row{
			{"tags": `[{"key":"http.response.status_code","type":"int64","value":200}]`},
			{"tags": []any{map[string]any{"key": "http.response.status_code", "type": "int64", "value": 503}}},
			{"tags": `[{"key":"other","value":"ignored"}]`},
		}})

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "cel-json-key-values",
			Provider: query.ProviderConfig{Type: "cel-json-key-values"},
			Columns: []query.ColumnDef{{
				Name: "http.response.status_code",
				CEL:  `'tags' in row ? jsonpath("$[?(@.key == 'http.response.status_code')].value", type(row['tags']) == string ? row['tags'].JSONArray() : row['tags']) : ''`,
			}},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows[0]["http.response.status_code"]).To(BeNumerically("==", 200))
		Expect(result.Rows[1]["http.response.status_code"]).To(BeNumerically("==", 503))
		Expect(result.Rows[2]).To(HaveKeyWithValue("http.response.status_code", ""))
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

	It("preserves native server filter keys in clicky JSON", func() {
		filtered := &query.Result{
			Rows:             []query.Row{{"name": "alpha"}},
			ColumnFilterKeys: map[string]string{"name": "filter.name"},
		}
		out, err := filtered.Render([]query.ColumnDef{{Name: "name"}}, "clicky-json")
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(ContainSubstring(`"filterKey": "filter.name"`))
	})

	It("renders Unit after Format and preserves both in clicky JSON", func() {
		formatted := &query.Result{Rows: []query.Row{{"ratio": 0.42}}}
		out, err := formatted.Render([]query.ColumnDef{{
			Name: "ratio", Type: query.ColumnTypeNumber, Format: "currency", Unit: "percentunit",
		}}, "clicky-json")
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(ContainSubstring(`"format": "currency"`))
		Expect(out).To(ContainSubstring(`"unit": "percentunit"`))
		Expect(out).To(ContainSubstring(`"plain": "42%"`))
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
