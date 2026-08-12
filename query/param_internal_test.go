package query

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("paramRoles", func() {
	It("indexes declared roles and defaults an unset role to filter", func() {
		Expect(paramRoles([]ParamDef{
			{Name: "service"},
			{Name: "since", Role: ParamRoleTimeFrom},
			{Name: "until", Role: ParamRoleTimeTo},
			{Name: "rows", Role: ParamRoleLimit},
			{Name: "skip", Role: ParamRoleOffset},
		})).To(Equal(map[string]ParamRole{
			"service": ParamRoleFilter,
			"since":   ParamRoleTimeFrom,
			"until":   ParamRoleTimeTo,
			"rows":    ParamRoleLimit,
			"skip":    ParamRoleOffset,
		}))
	})

	It("returns an empty index for a profile with no params", func() {
		Expect(paramRoles(nil)).To(BeEmpty())
	})
})

var _ = Describe("date param coercion", func() {
	DescribeTable("accepts Go date math and lenient date formats",
		func(input string) {
			value, err := (ParamDef{Name: "start", Type: ParamTypeDate}).coerce(input)
			Expect(err).ToNot(HaveOccurred())
			Expect(value).To(Equal(input))
		},
		Entry("date only", "2026-07-01"),
		Entry("date math", "now-7d"),
		Entry("timestamp without timezone", "2026-07-01T14:30:00"),
		Entry("space-separated timestamp", "2026-07-01 14:30:00"),
		Entry("Unix timestamp", "1782916200"),
	)

	It("rejects a value that is neither date math nor a recognized date", func() {
		_, err := (ParamDef{Name: "start", Type: ParamTypeDate}).coerce("not-a-date")
		Expect(err).To(MatchError(`value "not-a-date" is not a valid date or date math expression`))
	})
})
