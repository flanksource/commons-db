package query

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = DescribeTable("ColumnDef.clickyFormat",
	func(col ColumnDef, expected string) {
		Expect(col.clickyFormat()).To(Equal(expected))
	},
	Entry("datetime -> date", ColumnDef{Type: ColumnTypeDateTime}, "date"),
	Entry("duration -> duration", ColumnDef{Type: ColumnTypeDuration}, "duration"),
	Entry("bytes -> bytes", ColumnDef{Type: ColumnTypeBytes}, "bytes"),
	Entry("number -> float", ColumnDef{Type: ColumnTypeNumber}, "float"),
	Entry("string -> empty", ColumnDef{Type: ColumnTypeString}, ""),
	Entry("explicit Format overrides Type", ColumnDef{Type: ColumnTypeNumber, Format: "currency"}, "currency"),
)
