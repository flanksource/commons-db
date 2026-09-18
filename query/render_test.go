package query_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/flanksource/clicky/api"
	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type durationPresenter struct{}

func (durationPresenter) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("duration").FilterKey("filter.duration").Build(),
		api.Column("detail").Hidden().Build(),
	}
}

func (durationPresenter) Present(row query.Row) (map[string]any, error) {
	return map[string]any{
		"duration": api.TableCell{
			Value:       api.Text{Content: "125ms", Style: "text-red-500"},
			FilterValue: row["duration"],
		},
		"detail": row["detail"],
	}, nil
}

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

	DescribeTable("rejects projections that would make a row cyclic",
		func(profile query.Profile, expectedContext string) {
			query.RegisterProvider(&mockProvider{typ: "cyclic-projection", rows: []query.Row{{
				"metadata": map[string]any{"service": "api"},
			}}})
			profile.Provider = query.ProviderConfig{Type: "cyclic-projection"}

			_, err := query.Execute(context.New(), profile)
			Expect(err).To(MatchError(And(
				ContainSubstring(expectedContext),
				ContainSubstring("references its destination container"),
			)))
		},
		Entry("from a CEL column", query.Profile{
			Name:    "cyclic-cel-column",
			Columns: []query.ColumnDef{{Name: "snapshot", CEL: "row"}},
		}, `row 0: column "snapshot"`),
		Entry("from a root JSONPath column", query.Profile{
			Name:    "cyclic-jsonpath-column",
			Columns: []query.ColumnDef{{Name: "snapshot", JSONPath: "$"}},
		}, `row 0: column "snapshot"`),
		Entry("from a nested alias", query.Profile{
			Name:    "cyclic-alias",
			Aliases: []query.AliasDef{{Name: "metadata.snapshot", CEL: "row.metadata"}},
		}, `row 0: alias "metadata.snapshot"`),
	)
})

var _ = Describe("JSONPath columns", func() {
	It("extracts a value from the row", func() {
		query.RegisterProvider(&mockProvider{typ: "jsonpath-row", rows: []query.Row{
			{"metadata": map[string]any{"user": map[string]any{"email": "alice@example.com"}}},
			{"message": "metadata omitted"},
		}})

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "jsonpath-row",
			Provider: query.ProviderConfig{Type: "jsonpath-row"},
			Columns:  []query.ColumnDef{{Name: "user.email", JSONPath: "$.metadata.user.email"}},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows[0]).To(HaveKeyWithValue("user.email", "alice@example.com"))
		Expect(result.Rows[1]).To(HaveKeyWithValue("user.email", BeNil()))
	})

	It("roots the path at Source when it is set", func() {
		query.RegisterProvider(&mockProvider{typ: "jsonpath-source", rows: []query.Row{
			{"metadata": map[string]any{"user": map[string]any{"email": "bob@example.com"}}},
		}})

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "jsonpath-source",
			Provider: query.ProviderConfig{Type: "jsonpath-source"},
			Columns:  []query.ColumnDef{{Name: "user.email", Source: "metadata", JSONPath: "$.user.email"}},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows[0]).To(HaveKeyWithValue("user.email", "bob@example.com"))
	})

	It("parses a Source holding a JSON-encoded object", func() {
		query.RegisterProvider(&mockProvider{typ: "jsonpath-encoded-object", rows: []query.Row{
			{"metadata": `{"user":{"email":"carol@example.com"}}`},
		}})

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "jsonpath-encoded-object",
			Provider: query.ProviderConfig{Type: "jsonpath-encoded-object"},
			Columns:  []query.ColumnDef{{Name: "user.email", Source: "metadata", JSONPath: "$.user.email"}},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows[0]).To(HaveKeyWithValue("user.email", "carol@example.com"))
	})

	It("parses a Source holding a JSON-encoded array and filters it", func() {
		query.RegisterProvider(&mockProvider{typ: "jsonpath-encoded-array", rows: []query.Row{
			{"tags": `[{"key":"http.response.status_code","value":200}]`},
			{"tags": []any{map[string]any{"key": "http.response.status_code", "value": 503}}},
			{"tags": `[{"key":"other","value":"ignored"}]`},
		}})

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "jsonpath-encoded-array",
			Provider: query.ProviderConfig{Type: "jsonpath-encoded-array"},
			Columns: []query.ColumnDef{{
				Name: "http.response.status_code", Source: "tags",
				JSONPath: "$[?(@.key == 'http.response.status_code')].value",
			}},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows[0]["http.response.status_code"]).To(BeNumerically("==", 200))
		Expect(result.Rows[1]["http.response.status_code"]).To(BeNumerically("==", 503))
		Expect(result.Rows[2]).To(HaveKeyWithValue("http.response.status_code", BeNil()))
	})

	It("leaves Source in the row rather than renaming or consuming it", func() {
		query.RegisterProvider(&mockProvider{typ: "jsonpath-keeps-source", rows: []query.Row{
			{"metadata": map[string]any{"first": "a", "second": "b"}},
		}})

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "jsonpath-keeps-source",
			Provider: query.ProviderConfig{Type: "jsonpath-keeps-source"},
			Columns: []query.ColumnDef{
				{Name: "first", Source: "metadata", JSONPath: "$.first"},
				{Name: "second", Source: "metadata", JSONPath: "$.second"},
				{Name: "metadata", Type: query.ColumnTypeJSON},
			},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows[0]).To(HaveKeyWithValue("first", "a"))
		Expect(result.Rows[0]).To(HaveKeyWithValue("second", "b"))
		Expect(result.Rows[0]).To(HaveKey("metadata"))
	})

	It("fails loudly on an invalid JSONPath expression", func() {
		query.RegisterProvider(&mockProvider{typ: "jsonpath-bad", rows: []query.Row{{"a": 1.0}}})

		_, err := query.Execute(context.New(), query.Profile{
			Name:     "jsonpath-bad",
			Provider: query.ProviderConfig{Type: "jsonpath-bad"},
			Columns:  []query.ColumnDef{{Name: "x", JSONPath: "$[?(@.a =="}},
		})
		Expect(err).To(HaveOccurred())
	})
})

type hostPresenter struct{}

func (hostPresenter) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("sessionId").Build(),
		api.Column("clientHost").Label("Host").DefaultHidden().Build(),
	}
}

func (hostPresenter) Present(row query.Row) (map[string]any, error) {
	return map[string]any{"sessionId": row["sessionId"], "clientHost": row["clientHost"]}, nil
}

// clickyColumnFlags decodes a clicky-json table's columns into name → the
// value of their "defaultHidden" key, or "absent" when the key is omitted.
func clickyColumnFlags(document string) map[string]any {
	var doc struct {
		Node struct {
			Columns []map[string]any `json:"columns"`
		} `json:"node"`
	}
	Expect(json.Unmarshal([]byte(document), &doc)).To(Succeed(), document)
	flags := map[string]any{}
	for _, column := range doc.Node.Columns {
		value, present := column["defaultHidden"]
		if !present {
			value = "absent"
		}
		flags[column["name"].(string)] = value
	}
	return flags
}

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

	It("uses a typed row presenter only for interactive clicky JSON", func() {
		presented := &query.Result{
			Rows:             []query.Row{{"duration": 125.0, "detail": map[string]any{"id": "span-1"}}},
			ColumnFilterKeys: map[string]string{"duration": "filter.duration"},
			Presenter:        durationPresenter{},
		}

		clickyJSON, err := presented.Render([]query.ColumnDef{{Name: "duration"}, {Name: "detail"}}, "clicky-json")
		Expect(err).ToNot(HaveOccurred())
		Expect(clickyJSON).To(And(
			ContainSubstring(`"text": "125ms"`),
			ContainSubstring(`"color": "#ef4444"`),
			ContainSubstring(`"filterValue": 125`),
			ContainSubstring(`"detail"`),
			ContainSubstring("span-1"),
		))

		rawJSON, err := presented.Render([]query.ColumnDef{{Name: "duration"}, {Name: "detail"}}, "json")
		Expect(err).ToNot(HaveOccurred())
		Expect(rawJSON).To(And(ContainSubstring(`125`), Not(ContainSubstring("125ms"))))
	})

	DescribeTable("keeps a presenter's DefaultHidden column listed and flagged in clicky JSON",
		func(rows []query.Row) {
			presented := &query.Result{Rows: rows, Presenter: hostPresenter{}}

			out, err := presented.Render([]query.ColumnDef{{Name: "sessionId"}, {Name: "clientHost"}}, "clicky-json")
			Expect(err).ToNot(HaveOccurred())
			Expect(clickyColumnFlags(out)).To(Equal(map[string]any{"sessionId": "absent", "clientHost": true}))
		},
		Entry("with rows", []query.Row{{"sessionId": 53, "clientHost": "azure-app-1"}}),
		Entry("with zero rows", []query.Row{}),
	)

	It("presents a reflected integer read back as a float64 without decimals, and a time cell as its zoned instant", func() {
		type capturedEvent struct {
			At        time.Time `json:"at" pretty:"kind=timestamp"`
			SessionID int       `json:"sessionId"`
			Duration  float64   `json:"durationMs" pretty:"type=duration,unit=ms"`
		}
		columns, err := query.ColumnsFor(reflect.TypeFor[capturedEvent]())
		Expect(err).ToNot(HaveOccurred())
		const capturedAt = "2026-09-15T15:55:01.132Z"

		rows, err := query.PresentClickyRows(query.Profile{Name: "events", Columns: columns}, []query.Row{
			{"at": capturedAt, "sessionId": float64(73), "durationMs": 2.012},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].Cells["sessionId"].Plain).To(Equal("73"))
		Expect(rows[0].Cells["at"].FilterValue).To(Equal(capturedAt))
		Expect(rows[0].Cells["durationMs"].Plain).To(Equal("2 ms"))
	})

	It("rejects a cyclic row before handing it to Clicky", func() {
		row := query.Row{"name": "alpha"}
		row["snapshot"] = row

		_, err := (&query.Result{Rows: []query.Row{row}}).Render(nil, "clicky-json")
		Expect(err).To(MatchError(And(
			ContainSubstring("row 0 cannot be rendered"),
			ContainSubstring("value contains a cycle"),
		)))
	})
})
