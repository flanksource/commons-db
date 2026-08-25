package query_test

import (
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A column's Style is a CEL expression over the row, evaluated per row into the
// presentation classes for that cell. It must colour the rendered table without
// altering the row an export reads.
var _ = Describe("Column styles", func() {
	profileWithStyle := func(providerType string) query.Profile {
		return query.Profile{
			Name:     "styled levels",
			Provider: query.ProviderConfig{Type: providerType},
			Columns: []query.ColumnDef{
				{Name: "level", Style: `level == "ERROR" ? "text-red-500" : ""`},
				{Name: "message"},
			},
		}
	}

	It("evaluates a style per row without touching the row", func() {
		provider := &mockProvider{typ: "style-eval", rows: []query.Row{
			{"level": "ERROR", "message": "boom"},
			{"level": "INFO", "message": "fine"},
		}}
		query.RegisterProvider(provider)

		result, err := query.Execute(context.New(), profileWithStyle(provider.typ))

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Styles).To(HaveLen(2))
		Expect(result.Styles[0]).To(HaveKeyWithValue("level", "text-red-500"))
		Expect(result.Styles[1]).To(BeEmpty(), "an empty class is not recorded")

		// The rows themselves are untouched, so an export carries plain values.
		Expect(result.Rows[0]).To(Equal(query.Row{"level": "ERROR", "message": "boom"}))
		Expect(result.Rows[1]).To(Equal(query.Row{"level": "INFO", "message": "fine"}))
	})

	It("carries the class into a rendered table but not into an export", func() {
		provider := &mockProvider{typ: "style-render", rows: []query.Row{
			{"level": "ERROR", "message": "boom"},
		}}
		query.RegisterProvider(provider)
		profile := profileWithStyle(provider.typ)

		result, err := query.Execute(context.New(), profile)
		Expect(err).ToNot(HaveOccurred())

		rendered, err := result.Render(profile.Columns, "html")
		Expect(err).ToNot(HaveOccurred())
		Expect(rendered).To(ContainSubstring("text-red-500"))
		Expect(rendered).To(ContainSubstring("ERROR"))

		// The promise of keeping styles beside the rows rather than inside them:
		// a data export carries the value and none of the presentation.
		exported, err := result.Render(profile.Columns, "csv")
		Expect(err).ToNot(HaveOccurred())
		Expect(exported).To(ContainSubstring("ERROR"))
		Expect(exported).ToNot(ContainSubstring("text-red-500"))
	})

	It("leaves Styles nil when no column declares one", func() {
		provider := &mockProvider{typ: "style-absent", rows: []query.Row{{"level": "INFO"}}}
		query.RegisterProvider(provider)

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "plain",
			Provider: query.ProviderConfig{Type: provider.typ},
			Columns:  []query.ColumnDef{{Name: "level"}},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Styles).To(BeNil())
	})

	It("fails loudly when a style expression is not a string", func() {
		provider := &mockProvider{typ: "style-non-string", rows: []query.Row{{"level": "ERROR"}}}
		query.RegisterProvider(provider)

		_, err := query.Execute(context.New(), query.Profile{
			Name:     "bad style",
			Provider: query.ProviderConfig{Type: provider.typ},
			Columns:  []query.ColumnDef{{Name: "level", Style: `level == "ERROR"`}},
		})

		Expect(err).To(MatchError(ContainSubstring("expected a string")))
	})

	It("keeps styles aligned with rows after filters drop some", func() {
		provider := &mockProvider{typ: "style-after-filter", rows: []query.Row{
			{"level": "INFO", "message": "dropped"},
			{"level": "ERROR", "message": "kept"},
		}}
		query.RegisterProvider(provider)

		profile := profileWithStyle(provider.typ)
		profile.Filters = []query.FilterDef{{
			Name: "errors", Hidden: true,
			Fields: map[string]string{"level": `level == "ERROR"`},
		}}

		result, err := query.Execute(context.New(), profile)

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(1))
		Expect(result.Styles).To(HaveLen(1))
		Expect(result.Styles[0]).To(HaveKeyWithValue("level", "text-red-500"))
	})
})
