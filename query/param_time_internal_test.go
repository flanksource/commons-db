package query

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("time range params bound to a field", func() {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	defs := []ParamDef{
		{Name: "from", Type: ParamTypeDateTime, Role: ParamRoleTimeFrom, Field: "at"},
		{Name: "to", Type: ParamTypeDateTime, Role: ParamRoleTimeTo, Field: "at"},
	}

	It("resolves each edge the request names into a half-open time range on the field", func() {
		resolved, filters, err := resolveParams(defs, map[string]any{"from": "now-12h", "to": "2026-09-14T14:00:00+02:00"}, now)
		Expect(err).ToNot(HaveOccurred())

		from, to := "2026-09-14T00:00:00Z", "2026-09-14T14:00:00+02:00"
		Expect(resolved).To(Equal(map[string]any{"from": from, "to": to}))
		Expect(filters).To(Equal([]ColumnFilterValue{
			{Key: "from", Field: "at", Kind: ColumnFilterKindTime, Range: &FilterRange{Min: &FilterBound{Value: from, Inclusive: true}}},
			{Key: "to", Field: "at", Kind: ColumnFilterKindTime, Range: &FilterRange{Max: &FilterBound{Value: to}}},
		}))
	})

	It("leaves an edge the request does not name open", func() {
		_, filters, err := resolveParams(defs, map[string]any{"to": "2026-09-14T10:00:00Z"}, now)
		Expect(err).ToNot(HaveOccurred())
		Expect(filters).To(Equal([]ColumnFilterValue{
			{Key: "to", Field: "at", Kind: ColumnFilterKindTime, Range: &FilterRange{Max: &FilterBound{Value: "2026-09-14T10:00:00Z"}}},
		}))
	})

	It("bounds by a default the request does not override", func() {
		withDefault := []ParamDef{{Name: "from", Type: ParamTypeDateTime, Role: ParamRoleTimeFrom, Field: "at", Default: "now-1h"}}
		_, filters, err := resolveParams(withDefault, nil, now)
		Expect(err).ToNot(HaveOccurred())
		Expect(filters).To(Equal([]ColumnFilterValue{
			{Key: "from", Field: "at", Kind: ColumnFilterKindTime, Range: &FilterRange{Min: &FilterBound{Value: "2026-09-14T11:00:00Z", Inclusive: true}}},
		}))
	})

	DescribeTable("validates where a field on a time param can go",
		func(providerType string, def ParamDef, message string) {
			err := Profile{Name: "p", Provider: ProviderConfig{Type: providerType}, Query: "select 1", Params: []ParamDef{def}}.Validate()
			if message == "" {
				Expect(err).ToNot(HaveOccurred())
				return
			}
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("a datetime time-from on sqlite", "sqlite", defs[0], ""),
		Entry("a time param on a provider that folds time roles itself", "opensearch", defs[0], "only a SQL provider"),
		Entry("a time param that is not a datetime", "sqlite",
			ParamDef{Name: "from", Type: ParamTypeString, Role: ParamRoleTimeFrom, Field: "at"}, "datetime"),
		Entry("a field that is not a column name", "sqlite",
			ParamDef{Name: "from", Type: ParamTypeDateTime, Role: ParamRoleTimeFrom, Field: "a.b"}, "plain column name"),
	)
})
