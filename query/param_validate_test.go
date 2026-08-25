package query_test

import (
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Profile.Validate on params", func() {
	profileWith := func(providerType string, params ...query.ParamDef) query.Profile {
		return query.Profile{
			Name:     "p",
			Provider: query.ProviderConfig{Type: providerType},
			Query:    "select 1",
			Params:   params,
		}
	}

	listParam := func(field string) query.ParamDef {
		return query.ParamDef{Name: "regions", Type: query.ParamTypeList, Field: field}
	}

	Describe("a declared exclusion must have somewhere to go", func() {
		It("rejects a bound list param on a provider that applies no native filters", func() {
			err := profileWith("prometheus", listParam("region")).Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(SatisfyAll(
				ContainSubstring("regions"),
				ContainSubstring("prometheus"),
			))
		})

		It("accepts a bound list param on opensearch", func() {
			Expect(profileWith("opensearch", listParam("region")).Validate()).To(Succeed())
		})

		It("accepts a bound list param on opentelemetry", func() {
			Expect(profileWith("opentelemetry", listParam("region")).Validate()).To(Succeed())
		})

		It("accepts a bound list param on sql, which filters over its result", func() {
			Expect(profileWith("sql", listParam("region")).Validate()).To(Succeed())
		})

		// A SQL filter narrows one result column, so a field that cannot be
		// quoted into a column reference could never be applied.
		It("rejects a sql-bound list param whose field is not a column name", func() {
			err := profileWith("postgres", listParam("payload.user")).Validate()
			Expect(err).To(MatchError(ContainSubstring("is not a plain column name")))
		})

		It("accepts an unbound list param on any provider, since it can hold no exclusion", func() {
			Expect(profileWith("prometheus", listParam("")).Validate()).To(Succeed())
		})
	})

	Describe("field belongs to a list", func() {
		It("rejects a field on a scalar param", func() {
			err := profileWith("opensearch", query.ParamDef{
				Name: "region", Type: query.ParamTypeEnum, Field: "region",
			}).Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("list"))
		})
	})

	Describe("param names", func() {
		It("rejects two params sharing a name", func() {
			err := profileWith("sql",
				query.ParamDef{Name: "region"}, query.ParamDef{Name: "region"}).Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("region"))
		})

		It("rejects a param that squats the column-filter prefix", func() {
			err := profileWith("sql", query.ParamDef{Name: "filter.service"}).Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("filter."))
		})
	})

	Describe("static options", func() {
		It("rejects an option that cannot survive the comma-joined wire form", func() {
			err := profileWith("opensearch", query.ParamDef{
				Name: "regions", Type: query.ParamTypeList, Field: "region",
				Options: []string{"us-east", "eu,west"},
			}).Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("eu,west"))
		})

		It("rejects an option whose leading ! would decode as an exclusion", func() {
			err := profileWith("opensearch", query.ParamDef{
				Name: "regions", Type: query.ParamTypeList, Field: "region",
				Options: []string{"!eu"},
			}).Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("!eu"))
		})
	})

	Describe("roles", func() {
		It("rejects a list param claiming a paging role", func() {
			err := profileWith("opensearch", query.ParamDef{
				Name: "rows", Type: query.ParamTypeList, Role: query.ParamRoleLimit,
			}).Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("rows"))
		})
	})

	Describe("SQL identifiers", func() {
		DescribeTable("accepts identifier params for SQL providers",
			func(providerType string) {
				Expect(profileWith(providerType, query.ParamDef{
					Name: "table", Type: query.ParamTypeIdentifier,
				}).Validate()).To(Succeed())
			},
			Entry("generic", "sql"),
			Entry("postgres", "postgres"),
			Entry("mysql", "mysql"),
			Entry("sqlserver", "sqlserver"),
			Entry("clickhouse", "clickhouse"),
			Entry("sqlite", "sqlite"),
		)

		It("rejects identifier params for non-SQL providers", func() {
			err := profileWith("prometheus", query.ParamDef{
				Name: "table", Type: query.ParamTypeIdentifier,
			}).Validate()
			Expect(err).To(MatchError(ContainSubstring("only supported by SQL providers")))
		})
	})
})
