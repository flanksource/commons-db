package query_test

import (
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("OpenSearch column filters", func() {
	It("maps direct and simple CEL columns while requiring an override for complex CEL", func() {
		profile := query.Profile{
			Provider: query.ProviderConfig{
				Type: "opentelemetry",
				Options: map[string]any{
					"serviceField": "process.serviceName",
				},
			},
			Columns: []query.ColumnDef{
				{Name: "service"},
				{Name: "service_name"},
				{Name: "method", CEL: `span["attributes.http.method"]`},
				{
					Name: "status", CEL: `jsonpath("$.status", row.payload)`,
					Filter: &query.ColumnFilterDef{Field: "attributes.http.status_code"},
				},
				{Name: "payload.user", CEL: `jsonpath("$.user", row.payload)`},
			},
		}

		bindings, err := profile.ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(Equal([]query.ColumnFilterBinding{
			{Column: "service", Key: "filter.service", Field: "process.serviceName", Label: "service", Kind: query.ColumnFilterKindTerms, Lookup: true, Multi: true},
			{Column: "service_name", Key: "filter.service_name", Field: "process.serviceName", Label: "service_name", Kind: query.ColumnFilterKindTerms, Lookup: true, Multi: true},
			{Column: "method", Key: "filter.method", Field: "attributes.http.method", Label: "method", Kind: query.ColumnFilterKindTerms, Lookup: true, Multi: true},
			{Column: "status", Key: "filter.status", Field: "attributes.http.status_code", Label: "status", Kind: query.ColumnFilterKindTerms, Lookup: true, Multi: true},
		}))
	})

	It("makes a literal JSONPath column filterable without an override", func() {
		// The CEL wrapper equivalent of these paths infers nothing, so this is
		// what a promoted JSON column gains by being declared as a jsonpath.
		profile := query.Profile{
			Provider: query.ProviderConfig{Type: "opensearch"},
			Columns: []query.ColumnDef{
				{Name: "user", JSONPath: "$.payload.user"},
				{Name: "status", Source: "payload", JSONPath: "$.status"},
				{Name: "code", Source: "tags", JSONPath: "$[?(@.key == 'http.status')].value"},
			},
		}

		bindings, err := profile.ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(Equal([]query.ColumnFilterBinding{
			{Column: "user", Key: "filter.user", Field: "payload.user", Label: "user", Kind: query.ColumnFilterKindTerms, Lookup: true, Multi: true},
			{Column: "status", Key: "filter.status", Field: "payload.status", Label: "status", Kind: query.ColumnFilterKindTerms, Lookup: true, Multi: true},
		}))
	})

	Describe("a tag column reading one entry of a repeated field", func() {
		tagColumn := func(filter *query.ColumnFilterDef) query.Profile {
			return query.Profile{
				Provider: query.ProviderConfig{Type: "opensearch"},
				Columns: []query.ColumnDef{
					{Name: "app", JSONPath: "$.tags[?(@.key == 'app')].value", Filter: filter},
				},
			}
		}

		It("pushes the selection down when the container is declared nested", func() {
			bindings, err := tagColumn(&query.ColumnFilterDef{Nested: "tags"}).ColumnFilterBindings()
			Expect(err).ToNot(HaveOccurred())
			Expect(bindings).To(Equal([]query.ColumnFilterBinding{{
				Column: "app", Key: "filter.app", Field: "tags.value", Label: "app",
				Nested: "tags", Where: map[string]string{"tags.key": "app"},
				Kind: query.ColumnFilterKindTerms, Lookup: true, Multi: true,
			}}))
		})

		// Without a nested mapping the key and the value are matched against the
		// whole document, so a document tagged app=web and env=prod would answer a
		// request for app=prod. Offering no filter is the only honest reading.
		It("offers no filter when the container is not declared nested", func() {
			bindings, err := tagColumn(nil).ColumnFilterBindings()
			Expect(err).ToNot(HaveOccurred())
			Expect(bindings).To(BeEmpty())
		})

		It("refuses a nested declaration naming a different container", func() {
			_, err := tagColumn(&query.ColumnFilterDef{Nested: "process.tags"}).ColumnFilterBindings()
			Expect(err).To(MatchError(ContainSubstring(`declares nested "process.tags" but its jsonpath picks an entry of "tags"`)))
		})

		It("refuses a where beside a path that already pins one", func() {
			_, err := tagColumn(&query.ColumnFilterDef{
				Nested: "tags", Where: map[string]string{"tags.key": "env"},
			}).ColumnFilterBindings()
			Expect(err).To(MatchError(ContainSubstring("already pins tags.key")))
		})

		It("refuses a container on a provider with no notion of one", func() {
			profile := query.Profile{
				Provider: query.ProviderConfig{Type: "postgres"},
				Columns: []query.ColumnDef{
					{Name: "app", Filter: &query.ColumnFilterDef{Field: "tags.value", Nested: "tags"}},
				},
			}
			_, err := profile.ColumnFilterBindings()
			Expect(err).To(MatchError(ContainSubstring(`nested "tags", which provider "postgres" has no equivalent of`)))
		})

		It("refuses a field its declared container does not hold", func() {
			profile := query.Profile{
				Provider: query.ProviderConfig{Type: "opensearch"},
				Columns: []query.ColumnDef{
					{Name: "app", Filter: &query.ColumnFilterDef{Field: "labels.app", Nested: "tags"}},
				},
			}
			_, err := profile.ColumnFilterBindings()
			Expect(err).To(MatchError(ContainSubstring(`field "labels.app" is not inside nested "tags"`)))
		})

		It("refuses a where with no container to be scoped to", func() {
			err := query.ColumnFilterDef{
				Field: "tags.value", Where: map[string]string{"tags.key": "app"},
			}.Validate("app")
			Expect(err).To(MatchError(ContainSubstring("sets where without nested")))
		})
	})

	It("keeps the provider's inferred field for a filter that only enumerates values", func() {
		// The OpenTelemetry naming table applies to an inferred field, so a
		// filter that declares options but no field must not lose it.
		profile := query.Profile{
			Provider: query.ProviderConfig{
				Type:    "opentelemetry",
				Options: map[string]any{"serviceField": "process.serviceName"},
			},
			Columns: []query.ColumnDef{{
				Name:   "service",
				Filter: &query.ColumnFilterDef{Options: []string{"api", "web"}},
			}},
		}

		bindings, err := profile.ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(Equal([]query.ColumnFilterBinding{{
			Column: "service", Key: "filter.service", Field: "process.serviceName", Label: "service",
			Kind: query.ColumnFilterKindTerms, Options: []string{"api", "web"}, Lookup: false, Multi: true,
		}}))
	})

	It("derives the control from the column type", func() {
		profile := query.Profile{
			Provider: query.ProviderConfig{Type: "opensearch"},
			Columns: []query.ColumnDef{
				{Name: "region", Type: query.ColumnTypeString},
				{Name: "latency", Type: query.ColumnTypeDuration},
				{Name: "size", Type: query.ColumnTypeBytes},
				{Name: "created", Type: query.ColumnTypeDateTime},
				{Name: "deleted", Type: query.ColumnTypeBoolean},
				{Name: "labels", Type: query.ColumnTypeKeyValues},
				{Name: "payload", Type: query.ColumnTypeJSON},
			},
		}

		bindings, err := profile.ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		kinds := map[string]query.ColumnFilterKind{}
		for _, binding := range bindings {
			kinds[binding.Column] = binding.Kind
		}
		Expect(kinds).To(Equal(map[string]query.ColumnFilterKind{
			"region":  query.ColumnFilterKindTerms,
			"latency": query.ColumnFilterKindDuration,
			"size":    query.ColumnFilterKindRange,
			"created": query.ColumnFilterKindTime,
			"deleted": query.ColumnFilterKindBoolean,
		}))
	})

	It("lets declared time roles own the timestamp column filter", func() {
		profile := query.Profile{
			Provider: query.ProviderConfig{Type: "opensearch"},
			Params: []query.ParamDef{
				{Name: "from", Type: query.ParamTypeDateTime, Role: query.ParamRoleTimeFrom},
				{Name: "to", Type: query.ParamTypeDateTime, Role: query.ParamRoleTimeTo},
			},
			Columns: []query.ColumnDef{
				{Name: "startTimeMillis", Kind: query.ColumnKindTimestamp},
				{Name: "created_at", Type: query.ColumnTypeDateTime},
			},
		}

		bindings, err := profile.ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(Equal([]query.ColumnFilterBinding{{
			Column: "created_at", Key: "filter.created_at", Field: "created_at", Label: "created_at",
			Kind: query.ColumnFilterKindTime,
		}}))
	})

	It("offers no lookup for a filter whose values are typed rather than picked", func() {
		profile := query.Profile{
			Provider: query.ProviderConfig{Type: "opensearch"},
			Columns: []query.ColumnDef{
				{Name: "latency", Type: query.ColumnTypeNumber},
				{Name: "trace_id", Filter: &query.ColumnFilterDef{Lookup: lo.ToPtr(false)}},
			},
		}

		bindings, err := profile.ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings[0].Lookup).To(BeFalse())
		Expect(bindings[1].Lookup).To(BeFalse())
	})

	// Enumerating identifiers scans the whole result to answer with a page of
	// the rows, so a UUID column is typed into rather than picked from.
	It("offers no value list for an identifier column", func() {
		profile := query.Profile{
			Provider: query.ProviderConfig{Type: "sql"},
			Columns:  []query.ColumnDef{{Name: "id", Type: query.ColumnTypeUUID}},
		}

		bindings, err := profile.ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(Equal([]query.ColumnFilterBinding{{
			Column: "id", Key: "filter.id", Field: "id", Label: "id",
			Kind: query.ColumnFilterKindExact, Lookup: false, Multi: true,
		}}))
		// The values still compare exactly and several still travel at once; only
		// the list is gone, which is what the control has always rendered.
		Expect(bindings[0].ControlType()).To(Equal("value"))
	})

	// An exact match's defining property is that it has no list, so a lookup on
	// one is a request nothing can honour. Ignoring it is what turned "I asked
	// for a dropdown" into "there is no dropdown, and nothing said why".
	It("refuses a value list on a filter that has none to offer", func() {
		profile := query.Profile{
			Provider: query.ProviderConfig{Type: "sql"},
			Columns: []query.ColumnDef{{
				Name: "tenant_id", Type: query.ColumnTypeUUID,
				Filter: &query.ColumnFilterDef{Lookup: lo.ToPtr(true)},
			}},
		}

		_, err := profile.ColumnFilterBindings()
		Expect(err).To(MatchError(ContainSubstring("declare kind: terms to enumerate them")))
	})

	It("lets an author put the value list back on an identifier column", func() {
		profile := query.Profile{
			Provider: query.ProviderConfig{Type: "sql"},
			Columns: []query.ColumnDef{{
				Name: "tenant_id", Type: query.ColumnTypeUUID,
				Filter: &query.ColumnFilterDef{
					Kind: query.ColumnFilterKindTerms, Lookup: lo.ToPtr(true),
				},
			}},
		}

		bindings, err := profile.ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings[0].Lookup).To(BeTrue())
		Expect(bindings[0].ControlType()).To(Equal("multi-filter"))
	})

	// A duration bound is resolved into the unit the column stores, so a unit
	// nothing can convert into is refused where it was written rather than on
	// the first request that types "5s".
	It("refuses a duration filter whose unit is not a time unit", func() {
		profile := query.Profile{
			Provider: query.ProviderConfig{Type: "sql"},
			Columns: []query.ColumnDef{
				{Name: "latency", Type: query.ColumnTypeDuration, Unit: "percent"},
			},
		}

		_, err := profile.ColumnFilterBindings()
		Expect(err).To(MatchError(ContainSubstring("needs a time unit")))
	})

	It("carries the column's unit onto a duration binding", func() {
		profile := query.Profile{
			Provider: query.ProviderConfig{Type: "sql"},
			Columns: []query.ColumnDef{
				{Name: "latency", Type: query.ColumnTypeDuration, Unit: "s"},
			},
		}

		bindings, err := profile.ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings[0].Unit).To(Equal("s"))
		Expect(bindings[0].Multi).To(BeFalse())
		Expect(bindings[0].ControlType()).To(Equal("duration"))
	})

	// Only a value selection holds several values. Announcing a range or a time
	// bound as multi is what made the browser render it as a list to type into
	// instead of the control it is.
	It("takes several values only for a value selection", func() {
		profile := query.Profile{
			Provider: query.ProviderConfig{Type: "opensearch"},
			Columns: []query.ColumnDef{
				{Name: "region", Type: query.ColumnTypeString},
				{Name: "latency", Type: query.ColumnTypeNumber},
				{Name: "created", Type: query.ColumnTypeDateTime},
				{Name: "deleted", Type: query.ColumnTypeBoolean},
				{Name: "message", Filter: &query.ColumnFilterDef{Kind: query.ColumnFilterKindText}},
			},
		}

		bindings, err := profile.ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		multi := map[string]bool{}
		for _, binding := range bindings {
			multi[binding.Column] = binding.Multi
		}
		Expect(multi).To(Equal(map[string]bool{
			"region": true, "latency": false, "created": false, "deleted": false, "message": false,
		}))
	})

	It("removes a disabled column's filter while keeping the column", func() {
		profile := query.Profile{
			Provider: query.ProviderConfig{Type: "opensearch"},
			Columns: []query.ColumnDef{
				{Name: "region"},
				{Name: "note", Filter: &query.ColumnFilterDef{Disabled: true}},
			},
		}

		bindings, err := profile.ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(HaveLen(1))
		Expect(bindings[0].Column).To(Equal("region"))
	})

	It("keeps renamed column filters bound to the provider field", func() {
		profile := query.Profile{
			Provider: query.ProviderConfig{Type: "opensearch"},
			Columns: []query.ColumnDef{{
				Name: "service", Source: "service.name", Type: query.ColumnTypeString,
			}},
		}

		bindings, err := profile.ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(Equal([]query.ColumnFilterBinding{{
			Column: "service", Key: "filter.service", Field: "service.name", Label: "service",
			Kind: query.ColumnFilterKindTerms, Lookup: true, Multi: true,
		}}))
	})

	It("does not advertise native column filters for a provider that applies none", func() {
		bindings, err := (query.Profile{
			Provider: query.ProviderConfig{Type: "prometheus"},
			Columns:  []query.ColumnDef{{Name: "status"}},
		}).ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(BeEmpty())
	})

	It("requires a backend field only when the column implies none", func() {
		// An empty filter on a direct column still resolves through the column
		// itself; only a computed value has nothing to push the selection to.
		bindings, err := (query.Profile{
			Name:     "direct",
			Provider: query.ProviderConfig{Type: "opensearch"},
			Columns:  []query.ColumnDef{{Name: "status", Filter: &query.ColumnFilterDef{}}},
		}).ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(HaveLen(1))
		Expect(bindings[0].Field).To(Equal("status"))

		computed, err := (query.Profile{
			Name:     "computed",
			Provider: query.ProviderConfig{Type: "opensearch"},
			Columns: []query.ColumnDef{{
				Name: "status", CEL: `jsonpath("$.status", row.payload)`,
				Filter: &query.ColumnFilterDef{},
			}},
		}).ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(computed).To(BeEmpty())
	})

	It("rejects filter options that the wire form cannot carry", func() {
		for _, option := range []string{"a,b", "!a"} {
			err := (query.Profile{
				Name:     "invalid",
				Provider: query.ProviderConfig{Type: "opensearch"},
				Columns: []query.ColumnDef{{
					Name: "status", Filter: &query.ColumnFilterDef{Options: []string{option}},
				}},
			}).Validate()
			Expect(err).To(HaveOccurred(), "option %q must be rejected", option)
		}
	})

	It("rejects filter options on a filter that selects no values", func() {
		err := (query.Profile{
			Name:     "invalid",
			Provider: query.ProviderConfig{Type: "opensearch"},
			Columns: []query.ColumnDef{{
				Name:   "latency",
				Filter: &query.ColumnFilterDef{Kind: query.ColumnFilterKindRange, Options: []string{"1"}},
			}},
		}).Validate()
		Expect(err).To(MatchError(ContainSubstring("filter options require")))
	})

	It("rejects a declared parameter that conflicts with a native column filter", func() {
		err := (query.Profile{
			Name:     "conflict",
			Provider: query.ProviderConfig{Type: "opensearch"},
			Params:   []query.ParamDef{{Name: "filter.status"}},
			Columns:  []query.ColumnDef{{Name: "status"}},
		}).Validate()
		Expect(err).To(MatchError(ContainSubstring("conflicts with native column filter")))
	})
})
