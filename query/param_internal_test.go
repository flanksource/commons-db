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
