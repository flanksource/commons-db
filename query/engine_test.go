package query_test

import (
	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

// mockProvider is a Provider that records the request it was given and returns
// a fixed set of rows, so engine dispatch can be tested without a backend.
type mockProvider struct {
	typ           string
	rows          []query.Row
	last          query.ProviderRequest
	lastNamespace string
}

func (m *mockProvider) Type() string { return m.typ }

func (m *mockProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	m.last = req
	m.lastNamespace = ctx.GetNamespace()
	return m.rows, nil
}

var _ = Describe("provider registry", func() {
	It("registers and resolves a provider by type", func() {
		p := &mockProvider{typ: "registry-roundtrip"}
		query.RegisterProvider(p)

		got, err := query.GetProvider("registry-roundtrip")
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(BeIdenticalTo(query.Provider(p)))
		Expect(query.RegisteredProviders()).To(ContainElement("registry-roundtrip"))
	})

	It("errors with the available types when the provider is unknown", func() {
		query.RegisterProvider(&mockProvider{typ: "known-one"})

		_, err := query.GetProvider("does-not-exist")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("no data provider registered"))
		Expect(err.Error()).To(ContainSubstring("known-one"))
	})
})

var _ = Describe("Profile YAML", func() {
	const connectionName = "oi" + "pa"
	const spec = `
profile: SQL Server trace
provider:
  type: sql
  connection: connection://` + connectionName + `
  options:
    driver: sqlserver
query: select 1
columns:
  - name: Duration
    type: duration
    cel: row.duration_ms / 1000
  - name: Secret
    hidden: true
processors:
  - type: sqlite.merge
    config:
      on: [FileID]
context:
  Policy:
    provider:
      type: sql
    query: select policy
output: [table, html]
`

	It("unmarshals the full declarative spec", func() {
		var p query.Profile
		Expect(yaml.Unmarshal([]byte(spec), &p)).To(Succeed())

		Expect(p.Name).To(Equal("SQL Server trace"))
		Expect(p.Provider.Type).To(Equal("sql"))
		Expect(p.Provider.Connection).To(Equal("connection://" + connectionName))
		Expect(p.Provider.Options).To(HaveKeyWithValue("driver", "sqlserver"))
		Expect(p.Query).To(Equal("select 1"))

		Expect(p.Columns).To(HaveLen(2))
		Expect(p.Columns[0].Name).To(Equal("Duration"))
		Expect(p.Columns[0].Type).To(Equal(query.ColumnTypeDuration))
		Expect(p.Columns[0].CEL).To(Equal("row.duration_ms / 1000"))
		Expect(p.Columns[1].Hidden).To(BeTrue())

		Expect(p.Processors).To(HaveLen(1))
		Expect(p.Processors[0].Type).To(Equal("sqlite.merge"))

		Expect(p.Context).To(HaveKey("Policy"))
		Expect(p.Context["Policy"].Provider.Type).To(Equal("sql"))
		Expect(p.Context["Policy"].Query).To(Equal("select policy"))

		Expect(p.Output).To(Equal([]string{"table", "html"}))
	})
})

var _ = Describe("Execute", func() {
	It("dispatches to the provider and returns its rows", func() {
		rows := []query.Row{{"id": 1}, {"id": 2}}
		query.RegisterProvider(&mockProvider{typ: "exec-primary", rows: rows})

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "trace",
			Provider: query.ProviderConfig{Type: "exec-primary", Connection: "conn"},
			Query:    "select rows",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Profile).To(Equal("trace"))
		Expect(result.Rows).To(Equal(rows))
	})

	It("scopes primary and context providers to the profile namespace", func() {
		primary := &mockProvider{typ: "exec-namespaced-primary"}
		secondary := &mockProvider{typ: "exec-namespaced-secondary"}
		query.RegisterProvider(primary)
		query.RegisterProvider(secondary)

		_, err := query.Execute(context.New(), query.Profile{
			Name:      "namespaced",
			Namespace: "prod",
			Provider:  query.ProviderConfig{Type: primary.typ},
			Context: map[string]query.SubQuery{
				"secondary": {Provider: query.ProviderConfig{Type: secondary.typ}},
			},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(primary.lastNamespace).To(Equal("prod"))
		Expect(secondary.lastNamespace).To(Equal("prod"))
	})

	It("runs context SubQueries into named side objects", func() {
		query.RegisterProvider(&mockProvider{typ: "exec-main", rows: []query.Row{{"id": 1}}})
		policyRows := []query.Row{{"policy": "P-1"}}
		query.RegisterProvider(&mockProvider{typ: "exec-policy", rows: policyRows})

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "trace",
			Provider: query.ProviderConfig{Type: "exec-main"},
			Context: map[string]query.SubQuery{
				"Policy": {Provider: query.ProviderConfig{Type: "exec-policy"}, Query: "select policy"},
			},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Context).To(HaveKeyWithValue("Policy", policyRows))
	})

	It("returns the available providers when the type is unregistered", func() {
		_, err := query.Execute(context.New(), query.Profile{
			Name:     "trace",
			Provider: query.ProviderConfig{Type: "missing-provider"},
		})
		Expect(err).To(MatchError(ContainSubstring("no data provider registered")))
	})
})
