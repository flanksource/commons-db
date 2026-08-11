package query_test

import (
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

// Filters are named row predicates evaluated after aliases, so a profile can
// trim known noise (access logs, health checks) without wrapping every column
// in a conditional. Hidden filters are always on; the rest are opt-in by name
// so a quick filter like `level == "ERROR"` cannot silently hide the default
// output.
var _ = Describe("Profile filters", func() {
	logRows := []query.Row{
		{"logger": "AccessLog", "message": "GET /health => 200"},
		{"logger": "AccessLog", "message": "GET /policy => 500"},
		{"logger": "app", "message": "boot complete"},
	}

	It("drops rows matching a hidden exclude filter", func() {
		provider := &mockProvider{typ: "filters-hidden-exclude", rows: append([]query.Row{}, logRows...)}
		query.RegisterProvider(provider)

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "hidden exclude",
			Provider: query.ProviderConfig{Type: provider.typ},
			Filters: []query.FilterDef{{
				Name:    "drop-access-log-2xx",
				Exclude: true,
				Hidden:  true,
				Fields: map[string]string{
					"logger": `row.logger == "AccessLog"`,
					"status": `string(row.message).contains("=> 2")`,
				},
			}},
			Columns: []query.ColumnDef{{Name: "message"}},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(2))
		Expect(result.Rows[0]).To(HaveKeyWithValue("message", "GET /policy => 500"))
		Expect(result.Rows[1]).To(HaveKeyWithValue("message", "boot complete"))
	})

	It("carries a quick filter without applying it", func() {
		provider := &mockProvider{typ: "filters-quick", rows: append([]query.Row{}, logRows...)}
		query.RegisterProvider(provider)

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "errors only",
			Provider: query.ProviderConfig{Type: provider.typ},
			Filters: []query.FilterDef{{
				Name:   "errors-only",
				Fields: map[string]string{"level": `string(row.message).contains("500")`},
			}},
			Columns: []query.ColumnDef{{Name: "message"}},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(3), "a quick filter is for a UI to offer, not for the engine to apply")
	})

	It("keeps only matching rows when exclude is false", func() {
		provider := &mockProvider{typ: "filters-include", rows: append([]query.Row{}, logRows...)}
		query.RegisterProvider(provider)

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "access log only",
			Provider: query.ProviderConfig{Type: provider.typ},
			Filters: []query.FilterDef{{
				Name:   "access-log-only",
				Hidden: true,
				Fields: map[string]string{"logger": `row.logger == "AccessLog"`},
			}},
			Columns: []query.ColumnDef{{Name: "logger"}},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(2))
	})

	It("evaluates filters against alias output, not just raw provider fields", func() {
		// `level` exists only because the alias makes it, so a filter that reads
		// it can only pass if aliases ran first.
		provider := &mockProvider{typ: "filters-after-aliases", rows: []query.Row{
			{"sev": "INFO"},
			{"sev": "ERROR"},
		}}
		query.RegisterProvider(provider)

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "alias filter",
			Provider: query.ProviderConfig{Type: provider.typ},
			Aliases:  []query.AliasDef{{Name: "level", CEL: `row.sev`}},
			Filters: []query.FilterDef{{
				Name:   "errors",
				Hidden: true,
				Fields: map[string]string{"level": `row.level == "ERROR"`},
			}},
			Columns: []query.ColumnDef{{Name: "level"}},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(1))
		Expect(result.Rows[0]).To(HaveKeyWithValue("level", "ERROR"))
	})

	// Legacy trace profiles name fields bare, exactly as their columns do, so a
	// converted filter has to read the same way without being rewritten.
	It("accepts bare field names as well as row/span qualified ones", func() {
		provider := &mockProvider{typ: "filters-bare-names", rows: append([]query.Row{}, logRows...)}
		query.RegisterProvider(provider)

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "bare names",
			Provider: query.ProviderConfig{Type: provider.typ},
			Filters: []query.FilterDef{{
				Name:    "drop-access-log",
				Exclude: true,
				Hidden:  true,
				Fields:  map[string]string{"logger": `logger == "AccessLog"`},
			}},
			Columns: []query.ColumnDef{{Name: "logger"}},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(1))
		Expect(result.Rows[0]).To(HaveKeyWithValue("logger", "app"))
	})

	It("fails loudly when a predicate is not a boolean", func() {
		provider := &mockProvider{typ: "filters-non-bool", rows: append([]query.Row{}, logRows...)}
		query.RegisterProvider(provider)

		_, err := query.Execute(context.New(), query.Profile{
			Name:     "bad predicate",
			Provider: query.ProviderConfig{Type: provider.typ},
			Filters: []query.FilterDef{{
				Name:   "not-a-predicate",
				Hidden: true,
				Fields: map[string]string{"logger": `logger`},
			}},
			Columns: []query.ColumnDef{{Name: "logger"}},
		})

		Expect(err).To(MatchError(ContainSubstring("expected a boolean")))
	})

	It("rejects a filter with no fields rather than silently keeping every row", func() {
		err := query.Profile{
			Name:     "empty filter",
			Provider: query.ProviderConfig{Type: "sql"},
			Query:    "select 1",
			Filters:  []query.FilterDef{{Name: "noop"}},
		}.Validate()

		Expect(err).To(MatchError(ContainSubstring("noop")))
		Expect(err).To(MatchError(ContainSubstring("fields")))
	})

	It("unmarshals filters from YAML", func() {
		var p query.Profile
		Expect(yaml.Unmarshal([]byte(`
profile: oipa logs
provider:
  type: k8s
filters:
  - name: drop-access-log-2xx
    description: Drop successful access log lines
    exclude: true
    hidden: true
    fields:
      logger: 'row.logger == "AccessLog"'
`), &p)).To(Succeed())

		Expect(p.Filters).To(HaveLen(1))
		Expect(p.Filters[0].Name).To(Equal("drop-access-log-2xx"))
		Expect(p.Filters[0].Description).To(Equal("Drop successful access log lines"))
		Expect(p.Filters[0].Exclude).To(BeTrue())
		Expect(p.Filters[0].Hidden).To(BeTrue())
		Expect(p.Filters[0].Fields).To(HaveKeyWithValue("logger", `row.logger == "AccessLog"`))
	})
})
