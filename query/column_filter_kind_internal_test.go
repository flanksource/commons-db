package query

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("column filter kind", func() {
	DescribeTable("derives the control from the column type",
		func(columnType ColumnType, expected ColumnFilterKind) {
			Expect(columnFilterKindFor(ColumnDef{Type: columnType})).To(Equal(expected))
		},
		Entry("an undeclared type", ColumnType(""), ColumnFilterKindTerms),
		Entry("string", ColumnTypeString, ColumnFilterKindTerms),
		Entry("status", ColumnTypeStatus, ColumnFilterKindTerms),
		Entry("health", ColumnTypeHealth, ColumnFilterKindTerms),
		Entry("number", ColumnTypeNumber, ColumnFilterKindRange),
		Entry("duration", ColumnTypeDuration, ColumnFilterKindDuration),
		// A size literal has two bases, so "5MB" would mean two numbers; only
		// duration has one unambiguous grammar to offer.
		Entry("bytes", ColumnTypeBytes, ColumnFilterKindRange),
		Entry("datetime", ColumnTypeDateTime, ColumnFilterKindTime),
		Entry("boolean", ColumnTypeBoolean, ColumnFilterKindBoolean),
		// An identifier compares exactly like a string, but there is no list of
		// them worth offering: enumerating one answers with a page of the rows.
		Entry("uuid", ColumnTypeUUID, ColumnFilterKindExact),
		Entry("key_value", ColumnTypeKeyValue, ColumnFilterKindNone),
		Entry("key_values", ColumnTypeKeyValues, ColumnFilterKindNone),
		Entry("json", ColumnTypeJSON, ColumnFilterKindNone),
	)

	// The timestamp role is documented as the column the table's date-range
	// control filters through, and the schema offers it as "Role" beside a
	// separate Type. An author who sets only the role and leaves the type alone
	// otherwise gets a value selection — a control that cannot express a bound.
	DescribeTable("gives the timestamp role a time bound whatever the type says",
		func(columnType ColumnType, expected ColumnFilterKind) {
			column := ColumnDef{Type: columnType, Kind: ColumnKindTimestamp}
			Expect(columnFilterKindFor(column)).To(Equal(expected))
		},
		Entry("an undeclared type", ColumnType(""), ColumnFilterKindTime),
		Entry("string", ColumnTypeString, ColumnFilterKindTime),
		Entry("datetime", ColumnTypeDateTime, ColumnFilterKindTime),
	)

	// The other roles say how a cell renders, not what it compares as.
	It("leaves the tags and status roles filtering by their type", func() {
		Expect(columnFilterKindFor(ColumnDef{Type: ColumnTypeString, Kind: ColumnKindTags})).
			To(Equal(ColumnFilterKindTerms))
		Expect(columnFilterKindFor(ColumnDef{Type: ColumnTypeNumber, Kind: ColumnKindStatus})).
			To(Equal(ColumnFilterKindRange))
	})

	// A substring match and a day-granularity bound are choices an author makes:
	// no column type implies either, because every type they could apply to has
	// an exact comparison or a full instant available already.
	It("never infers a substring match or a date bound", func() {
		for _, columnType := range []ColumnType{
			ColumnTypeString, ColumnTypeStatus, ColumnTypeHealth, ColumnTypeNumber,
			ColumnTypeDuration, ColumnTypeBytes, ColumnTypeDateTime, ColumnTypeBoolean,
			ColumnTypeUUID,
		} {
			for _, kind := range []ColumnKind{"", ColumnKindTimestamp} {
				column := ColumnDef{Type: columnType, Kind: kind}
				Expect(columnFilterKindFor(column)).ToNot(Equal(ColumnFilterKindText))
				Expect(columnFilterKindFor(column)).ToNot(Equal(ColumnFilterKindDate))
			}
		}
	})

	// A kind that is declarable but not compilable would surface as a schema
	// option that fails on the first request naming it.
	It("compiles every kind it offers an author", func() {
		for _, kind := range ColumnFilterKindValues() {
			Expect(ColumnFilterKind(kind).Valid()).To(BeTrue(), kind)
		}
	})

	It("treats an unset kind as a value selection", func() {
		Expect(ColumnFilterKind("").Normalized()).To(Equal(ColumnFilterKindTerms))
		Expect(ColumnFilterKind("").Valid()).To(BeTrue())
	})

	It("rejects a kind it cannot compile", func() {
		Expect(ColumnFilterKind("regex").Valid()).To(BeFalse())
	})

	// Only a value selection has a list to offer; a range, a toggle, a substring
	// and an exact match are typed rather than picked.
	DescribeTable("reports which kinds the backend can enumerate",
		func(kind ColumnFilterKind, lookupable bool) {
			Expect(kind.Lookupable()).To(Equal(lookupable))
		},
		Entry("terms", ColumnFilterKindTerms, true),
		Entry("exact", ColumnFilterKindExact, false),
		Entry("range", ColumnFilterKindRange, false),
		Entry("duration", ColumnFilterKindDuration, false),
		Entry("time", ColumnFilterKindTime, false),
		Entry("date", ColumnFilterKindDate, false),
		Entry("boolean", ColumnFilterKindBoolean, false),
		Entry("text", ColumnFilterKindText, false),
	)

	// Nine kinds, five clauses: an exact match is a terms query, a duration is a
	// range, a date is a time. This is what lets a list param and an identifier
	// column bound to one field still merge into a single predicate.
	DescribeTable("maps each kind to the clause it compiles to",
		func(kind, compiled ColumnFilterKind) {
			Expect(kind.CompilesAs()).To(Equal(compiled))
		},
		Entry("an unset kind", ColumnFilterKind(""), ColumnFilterKindTerms),
		Entry("terms", ColumnFilterKindTerms, ColumnFilterKindTerms),
		Entry("exact", ColumnFilterKindExact, ColumnFilterKindTerms),
		Entry("range", ColumnFilterKindRange, ColumnFilterKindRange),
		Entry("duration", ColumnFilterKindDuration, ColumnFilterKindRange),
		Entry("time", ColumnFilterKindTime, ColumnFilterKindTime),
		Entry("date", ColumnFilterKindDate, ColumnFilterKindTime),
		Entry("text", ColumnFilterKindText, ColumnFilterKindText),
		Entry("boolean", ColumnFilterKindBoolean, ColumnFilterKindBoolean),
		Entry("none", ColumnFilterKindNone, ColumnFilterKindNone),
	)

	DescribeTable("maps each kind to the control it registers as",
		func(kind ColumnFilterKind, control string) {
			Expect(kind.ControlType()).To(Equal(control))
		},
		Entry("terms", ColumnFilterKindTerms, "multi-filter"),
		Entry("exact", ColumnFilterKindExact, "value"),
		Entry("range", ColumnFilterKindRange, "number"),
		Entry("duration", ColumnFilterKindDuration, "duration"),
		// Not "date": a time bound carries both edges under one key, and "date"
		// is clicky's single-instant input.
		Entry("time", ColumnFilterKindTime, "date-range"),
		// The same two-edged control with the clock taken off it.
		Entry("date", ColumnFilterKindDate, "day-range"),
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
		// An author who wrote kind: terms on a column that turned out to have
		// nothing to offer still gets an input rather than an empty dropdown.
		Entry("a selection with nothing to enumerate",
			ColumnFilterBinding{Kind: ColumnFilterKindTerms}, "value"),
		// An exact match arrives at the same control by declaring it outright.
		Entry("an exact match", ColumnFilterBinding{Kind: ColumnFilterKindExact}, "value"),
		Entry("a time bound", ColumnFilterBinding{Kind: ColumnFilterKindTime}, "date-range"),
		Entry("a date bound", ColumnFilterBinding{Kind: ColumnFilterKindDate}, "day-range"),
		Entry("a range", ColumnFilterBinding{Kind: ColumnFilterKindRange}, "number"),
		Entry("a duration", ColumnFilterBinding{Kind: ColumnFilterKindDuration}, "duration"),
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
