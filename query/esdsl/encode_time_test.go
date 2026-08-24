package esdsl

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// instant is the moment every case below encodes, chosen so each epoch unit
// lands on a different, independently checkable number.
var instant = time.Date(2026, 4, 19, 11, 30, 15, 500_000_000, time.UTC)

var _ = Describe("EncodeTimeBound", func() {
	// A tail bounds its polls at an instant it picks itself rather than at a
	// parameter somebody supplied, so it needs the same field spelling the
	// parameter path produces — hence one encoder rather than two.
	DescribeTable("spells an instant the way the field is mapped",
		func(mapping TimeFieldMapping, format TimeFieldFormat, expected any) {
			search := Search{TimeField: "@timestamp", TimeFieldFormat: format}
			encoded, err := EncodeTimeBound(instant, search, &mapping)
			Expect(err).ToNot(HaveOccurred())
			Expect(encoded).To(Equal(expected))
		},
		Entry("date", TimeFieldMapping{Type: "date"}, TimeFieldFormat(""), "2026-04-19T11:30:15.5Z"),
		Entry("date_nanos", TimeFieldMapping{Type: "date_nanos"}, TimeFieldFormat(""), "2026-04-19T11:30:15.5Z"),
		Entry("date with epoch millis format", TimeFieldMapping{Type: "date", Format: "epoch_millis"}, TimeFieldFormat(""), int64(1776598215500)),
		Entry("date with epoch seconds format", TimeFieldMapping{Type: "date", Format: "epoch_second"}, TimeFieldFormat(""), int64(1776598215)),
		Entry("epoch seconds", TimeFieldMapping{Type: "long"}, TimeFieldFormatEpochSecond, int64(1776598215)),
		Entry("epoch millis", TimeFieldMapping{Type: "long"}, TimeFieldFormatEpochMillis, int64(1776598215500)),
		Entry("epoch micros", TimeFieldMapping{Type: "long"}, TimeFieldFormatEpochMicros, int64(1776598215500000)),
		Entry("epoch nanos", TimeFieldMapping{Type: "long"}, TimeFieldFormatEpochNanos, int64(1776598215500000000)),
	)

	It("refuses a date mapping whose only format cannot encode an RFC3339 or epoch bound", func() {
		_, err := EncodeTimeBound(instant, Search{TimeField: "@timestamp"}, &TimeFieldMapping{Type: "date", Format: "yyyy-MM-dd"})
		Expect(err).To(MatchError(ContainSubstring(`unsupported date format "yyyy-MM-dd"`)))
	})

	It("refuses a numeric field that does not say which epoch unit it stores", func() {
		_, err := EncodeTimeBound(instant, Search{TimeField: "ts"}, &TimeFieldMapping{Type: "long"})
		Expect(err).To(MatchError(ContainSubstring("requires timeFieldFormat")))
	})

	It("refuses a date field that also declares an epoch unit", func() {
		_, err := EncodeTimeBound(instant,
			Search{TimeField: "@timestamp", TimeFieldFormat: TimeFieldFormatEpochMillis},
			&TimeFieldMapping{Type: "date"})
		Expect(err).To(MatchError(ContainSubstring("must not set timeFieldFormat")))
	})

	It("refuses a field whose mapping cannot hold an instant at all", func() {
		_, err := EncodeTimeBound(instant, Search{TimeField: "@timestamp"}, &TimeFieldMapping{Type: "keyword"})
		Expect(err).To(MatchError(ContainSubstring("incompatible OpenSearch mapping type")))
	})

	// The overflow guard is the reason the epoch path validates rather than
	// casting: a bound silently truncated to fit the field would filter on an
	// instant nobody chose.
	It("refuses an epoch value the mapped width cannot hold", func() {
		_, err := EncodeTimeBound(instant,
			Search{TimeField: "ts", TimeFieldFormat: TimeFieldFormatEpochMillis},
			&TimeFieldMapping{Type: "integer"})
		Expect(err).To(MatchError(ContainSubstring("overflows integer")))
	})

	It("refuses to encode against a field whose mapping was never resolved", func() {
		_, err := EncodeTimeBound(instant, Search{TimeField: "@timestamp"}, nil)
		Expect(err).To(MatchError(ContainSubstring("no resolved mapping")))
	})
})
