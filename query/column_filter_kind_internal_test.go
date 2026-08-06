package query

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("column filter kind", func() {
	DescribeTable("derives the control from the column type",
		func(columnType ColumnType, expected ColumnFilterKind) {
			Expect(columnFilterKindFor(columnType)).To(Equal(expected))
		},
		Entry("an undeclared type", ColumnType(""), ColumnFilterKindTerms),
		Entry("string", ColumnTypeString, ColumnFilterKindTerms),
		Entry("status", ColumnTypeStatus, ColumnFilterKindTerms),
		Entry("health", ColumnTypeHealth, ColumnFilterKindTerms),
		Entry("number", ColumnTypeNumber, ColumnFilterKindRange),
		Entry("duration", ColumnTypeDuration, ColumnFilterKindRange),
		Entry("bytes", ColumnTypeBytes, ColumnFilterKindRange),
		Entry("datetime", ColumnTypeDateTime, ColumnFilterKindTime),
		Entry("boolean", ColumnTypeBoolean, ColumnFilterKindBoolean),
		Entry("key_value", ColumnTypeKeyValue, ColumnFilterKindNone),
		Entry("key_values", ColumnTypeKeyValues, ColumnFilterKindNone),
		Entry("json", ColumnTypeJSON, ColumnFilterKindNone),
	)

	// A substring match is a choice an author makes: no column type implies one,
	// because every type it could apply to has an exact comparison available.
	It("never infers a substring match", func() {
		for _, columnType := range []ColumnType{
			ColumnTypeString, ColumnTypeStatus, ColumnTypeHealth, ColumnTypeNumber,
			ColumnTypeDuration, ColumnTypeBytes, ColumnTypeDateTime, ColumnTypeBoolean,
		} {
			Expect(columnFilterKindFor(columnType)).ToNot(Equal(ColumnFilterKindText))
		}
	})

	It("treats an unset kind as a value selection", func() {
		Expect(ColumnFilterKind("").Normalized()).To(Equal(ColumnFilterKindTerms))
		Expect(ColumnFilterKind("").Valid()).To(BeTrue())
	})

	It("rejects a kind it cannot compile", func() {
		Expect(ColumnFilterKind("regex").Valid()).To(BeFalse())
	})

	// Only a value selection has a list to offer; a range, a toggle and a
	// substring are typed rather than picked.
	DescribeTable("reports which kinds the backend can enumerate",
		func(kind ColumnFilterKind, lookupable bool) {
			Expect(kind.Lookupable()).To(Equal(lookupable))
		},
		Entry("terms", ColumnFilterKindTerms, true),
		Entry("range", ColumnFilterKindRange, false),
		Entry("time", ColumnFilterKindTime, false),
		Entry("boolean", ColumnFilterKindBoolean, false),
		Entry("text", ColumnFilterKindText, false),
	)

	DescribeTable("maps each kind to the control it registers as",
		func(kind ColumnFilterKind, control string) {
			Expect(kind.ControlType()).To(Equal(control))
		},
		Entry("terms", ColumnFilterKindTerms, "multi-filter"),
		Entry("range", ColumnFilterKindRange, "number"),
		Entry("time", ColumnFilterKindTime, "date"),
		Entry("boolean", ColumnFilterKindBoolean, "bool"),
		Entry("text", ColumnFilterKindText, ""),
	)

	DescribeTable("rejects an enumerated value the wire form cannot carry",
		func(option, message string) {
			Expect(validateFilterOptions([]string{option})).To(MatchError(ContainSubstring(message)))
		},
		Entry("a comma", "us,eu", "must not contain a comma"),
		Entry("a leading exclusion", "!eu", "must not start with !"),
		Entry("nothing at all", "  ", "must not be empty"),
	)
})
