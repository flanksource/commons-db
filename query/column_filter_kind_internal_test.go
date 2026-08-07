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
		// An identifier compares exactly like a string; only its option list
		// differs, and that is Enumerable's business rather than the kind's.
		Entry("uuid", ColumnTypeUUID, ColumnFilterKindTerms),
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
		// Not "date": a time bound carries both edges under one key, and "date"
		// is clicky's single-instant input.
		Entry("time", ColumnFilterKindTime, "date-range"),
		Entry("boolean", ColumnFilterKindBoolean, "bool"),
		Entry("text", ColumnFilterKindText, ""),
	)

	// Enumerating a column is only worth a round trip when its values repeat.
	DescribeTable("reports which column types are worth enumerating",
		func(columnType ColumnType, enumerable bool) {
			Expect(columnType.Enumerable()).To(Equal(enumerable))
		},
		Entry("an undeclared type", ColumnType(""), true),
		Entry("string", ColumnTypeString, true),
		Entry("status", ColumnTypeStatus, true),
		Entry("uuid", ColumnTypeUUID, false),
	)

	// The control depends on whether there is anything to enumerate, which the
	// kind alone does not say.
	DescribeTable("refines the control from the binding",
		func(binding ColumnFilterBinding, control string) {
			Expect(binding.ControlType()).To(Equal(control))
		},
		Entry("a selection the backend can enumerate",
			ColumnFilterBinding{Kind: ColumnFilterKindTerms, Lookup: true}, "multi-filter"),
		Entry("a selection the profile enumerates itself",
			ColumnFilterBinding{Kind: ColumnFilterKindTerms, Options: []string{"eu"}}, "multi-filter"),
		Entry("a selection with nothing to enumerate",
			ColumnFilterBinding{Kind: ColumnFilterKindTerms}, "value"),
		Entry("a time bound", ColumnFilterBinding{Kind: ColumnFilterKindTime}, "date-range"),
		Entry("a range", ColumnFilterBinding{Kind: ColumnFilterKindRange}, "number"),
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
