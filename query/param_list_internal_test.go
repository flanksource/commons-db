package query

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("list param coercion", func() {
	listDef := func(field string, options ...string) ParamDef {
		return ParamDef{Name: "regions", Type: ParamTypeList, Field: field, Options: options}
	}

	Describe("wire decoding", func() {
		It("reads the same values from a string, a []string and a []any", func() {
			for _, raw := range []any{"us-east,us-west", []string{"us-east", "us-west"}, []any{"us-east", "us-west"}} {
				include, exclude, err := listDef("region").coerceList(raw)
				Expect(err).ToNot(HaveOccurred())
				Expect(include).To(Equal([]string{"us-east", "us-west"}))
				Expect(exclude).To(BeEmpty())
			}
		})

		It("splits an exclusion out of a mixed selection", func() {
			include, exclude, err := listDef("region").coerceList("us-east,!eu,us-west")
			Expect(err).ToNot(HaveOccurred())
			Expect(include).To(Equal([]string{"us-east", "us-west"}))
			Expect(exclude).To(Equal([]string{"eu"}))
		})

		It("trims blanks and dedupes, preserving first-seen order", func() {
			include, _, err := listDef("region").coerceList(" us-west , us-east ,, us-west ")
			Expect(err).ToNot(HaveOccurred())
			Expect(include).To(Equal([]string{"us-west", "us-east"}))
		})
	})

	Describe("exclusions require a transport", func() {
		It("rejects an exclusion on a param that declares no backend field", func() {
			_, _, err := listDef("").coerceList("us-east,!eu")
			Expect(err).To(MatchError(ContainSubstring("field")))
		})

		It("accepts the same selection once a field is declared", func() {
			_, exclude, err := listDef("region").coerceList("us-east,!eu")
			Expect(err).ToNot(HaveOccurred())
			Expect(exclude).To(Equal([]string{"eu"}))
		})
	})

	Describe("Options validation", func() {
		It("rejects an included value outside the enum, naming the value", func() {
			_, _, err := listDef("region", "us-east", "eu").coerceList("us-east,mars")
			Expect(err).To(MatchError(ContainSubstring(`"mars"`)))
		})

		It("rejects an excluded value outside the enum", func() {
			_, _, err := listDef("region", "us-east", "eu").coerceList("us-east,!mars")
			Expect(err).To(MatchError(ContainSubstring(`"mars"`)))
		})

		It("accepts a selection drawn entirely from the enum", func() {
			include, exclude, err := listDef("region", "us-east", "eu").coerceList("us-east,!eu")
			Expect(err).ToNot(HaveOccurred())
			Expect(include).To(Equal([]string{"us-east"}))
			Expect(exclude).To(Equal([]string{"eu"}))
		})
	})

	Describe("Template", func() {
		It("rewrites every element on both sides of the selection", func() {
			def := listDef("region")
			def.Template = "{value}-api"
			include, exclude, err := def.coerceList("us-east,!eu")
			Expect(err).ToNot(HaveOccurred())
			Expect(include).To(Equal([]string{"us-east-api"}))
			Expect(exclude).To(Equal([]string{"eu-api"}))
		})
	})

	Describe("emptiness", func() {
		It("treats an empty slice as an unsupplied value so defaults apply", func() {
			Expect(isEmptyParam([]string{})).To(BeTrue())
			Expect(isEmptyParam([]any{})).To(BeTrue())
			Expect(isEmptyParam([]string{"a"})).To(BeFalse())
		})

		It("applies a list default when nothing is supplied", func() {
			def := listDef("region")
			def.Default = []any{"us-east", "eu"}
			resolved, _, err := resolveParams([]ParamDef{def}, nil, time.Now())
			Expect(err).ToNot(HaveOccurred())
			Expect(resolved["regions"]).To(Equal([]string{"us-east", "eu"}))
		})

		It("accepts a comma-joined string as the default", func() {
			def := listDef("region")
			def.Default = "us-east,eu"
			resolved, _, err := resolveParams([]ParamDef{def}, nil, time.Now())
			Expect(err).ToNot(HaveOccurred())
			Expect(resolved["regions"]).To(Equal([]string{"us-east", "eu"}))
		})

		It("fails a required list whose supplied value holds no usable values", func() {
			def := listDef("region")
			def.Required = true
			_, _, err := resolveParams([]ParamDef{def}, map[string]any{"regions": " , "}, time.Now())
			Expect(err).To(MatchError(ContainSubstring("required")))
		})

		It("counts an exclude-only selection as satisfying required", func() {
			def := listDef("region")
			def.Required = true
			resolved, filters, err := resolveParams([]ParamDef{def}, map[string]any{"regions": "!eu"}, time.Now())
			Expect(err).ToNot(HaveOccurred())
			Expect(resolved["regions"]).To(Equal([]string{}))
			Expect(filters).To(Equal([]ColumnFilterValue{{
				Key: "regions", Field: "region", Kind: ColumnFilterKindTerms,
				Include: []string{}, Exclude: []string{"eu"},
			}}))
		})
	})

	Describe("resolveParams filter output", func() {
		It("exposes only the includes to the template and routes both sides to a filter", func() {
			resolved, filters, err := resolveParams(
				[]ParamDef{listDef("region")}, map[string]any{"regions": "us-east,!eu"}, time.Now())
			Expect(err).ToNot(HaveOccurred())
			Expect(resolved).To(Equal(map[string]any{"regions": []string{"us-east"}}))
			Expect(filters).To(Equal([]ColumnFilterValue{{
				Key: "regions", Field: "region", Kind: ColumnFilterKindTerms,
				Include: []string{"us-east"}, Exclude: []string{"eu"},
			}}))
		})

		It("contributes no filter when the param declares no backend field", func() {
			resolved, filters, err := resolveParams(
				[]ParamDef{listDef("")}, map[string]any{"regions": "us-east,us-west"}, time.Now())
			Expect(err).ToNot(HaveOccurred())
			Expect(resolved).To(Equal(map[string]any{"regions": []string{"us-east", "us-west"}}))
			Expect(filters).To(BeEmpty())
		})
	})

	// The browser writes this wire form in listValueMerge.ts. These are the exact
	// strings that codec emits, so a change on either side that breaks the pair
	// fails here rather than silently filtering on the wrong values.
	Describe("the wire form the browser writes", func() {
		DescribeTable("decodes to the same selection the browser meant",
			func(wire string, include, exclude []string) {
				gotInclude, gotExclude, err := listDef("region").coerceList(wire)
				Expect(err).ToNot(HaveOccurred())
				Expect(gotInclude).To(Equal(include))
				Expect(gotExclude).To(Equal(exclude))
			},
			Entry("includes only", "a,c", []string{"a", "c"}, []string{}),
			Entry("includes then exclusions", "a,c,!b", []string{"a", "c"}, []string{"b"}),
			Entry("exclusions only", "!b", []string{}, []string{"b"}),
			Entry("nothing selected", "", []string{}, []string{}),
		)
	})

	Describe("scalar params are unaffected", func() {
		It("keeps coercing the existing types as before", func() {
			resolved, filters, err := resolveParams([]ParamDef{
				{Name: "region", Type: ParamTypeEnum, Options: []string{"US", "EU"}},
				{Name: "rows", Type: ParamTypeNumber},
				{Name: "live", Type: ParamTypeBoolean},
			}, map[string]any{"region": "EU", "rows": "50", "live": "true"}, time.Now())
			Expect(err).ToNot(HaveOccurred())
			Expect(resolved).To(Equal(map[string]any{"region": "EU", "rows": float64(50), "live": true}))
			Expect(filters).To(BeEmpty())
		})
	})
})
