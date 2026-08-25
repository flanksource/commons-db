package query

import (
	"time"

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

var _ = Describe("temporal param coercion", func() {
	now := time.Date(2026, time.July, 8, 16, 30, 45, 123000000, time.FixedZone("SAST", 2*60*60))

	DescribeTable("normalizes dates to calendar-day strings",
		func(input, expected string) {
			value, err := (ParamDef{Name: "start", Type: ParamTypeDate}).coerce(input, now)
			Expect(err).ToNot(HaveOccurred())
			Expect(value).To(Equal(expected))
		},
		Entry("date only", "2026-07-01", "2026-07-01"),
		Entry("offset timestamp", "2026-07-01T14:30:00+03:00", "2026-07-01"),
		Entry("date math", "now-7d", "2026-07-01"),
	)

	DescribeTable("normalizes datetimes to RFC3339Nano while retaining their offset",
		func(input, expected string) {
			value, err := (ParamDef{Name: "start", Type: ParamTypeDateTime}).coerce(input, now)
			Expect(err).ToNot(HaveOccurred())
			Expect(value).To(Equal(expected))
		},
		Entry(
			"fractional offset timestamp",
			"2026-07-01T14:30:00.123456+03:00",
			"2026-07-01T14:30:00.123456+03:00",
		),
		Entry("date math", "now-7d", "2026-07-01T14:30:45.123Z"),
	)

	DescribeTable("rejects invalid dates and datetimes",
		func(paramType ParamType) {
			_, err := (ParamDef{Name: "start", Type: paramType}).coerce("not-a-date", now)
			Expect(err).To(MatchError(`value "not-a-date" is not a valid date or date math expression`))
		},
		Entry("date", ParamTypeDate),
		Entry("datetime", ParamTypeDateTime),
	)
})

var _ = Describe("duration param coercion", func() {
	DescribeTable("converts whole-millisecond durations to int64 milliseconds",
		func(input string, expected int64) {
			value, err := (ParamDef{Name: "window", Type: ParamTypeDuration}).coerce(input, time.Time{})
			Expect(err).ToNot(HaveOccurred())
			Expect(value).To(Equal(expected))
		},
		Entry("compound duration", "1h30m", int64(5_400_000)),
		Entry("fractional day", "1.5d", int64(129_600_000)),
		Entry("negative duration", "-30m", int64(-1_800_000)),
		Entry("zero", "0", int64(0)),
	)

	It("rejects a duration that cannot be represented as whole milliseconds", func() {
		_, err := (ParamDef{Name: "window", Type: ParamTypeDuration}).coerce("500us", time.Time{})
		Expect(err).To(MatchError(ContainSubstring("whole number of milliseconds")))
	})

	It("rejects an invalid duration", func() {
		_, err := (ParamDef{Name: "window", Type: ParamTypeDuration}).coerce("later", time.Time{})
		Expect(err).To(MatchError(ContainSubstring("invalid duration")))
	})
})

var _ = Describe("identifier param coercion", func() {
	It("retains a qualified SQL name as a string", func() {
		value, err := (ParamDef{Name: "table", Type: ParamTypeIdentifier}).coerce("analytics.orders", time.Time{})
		Expect(err).ToNot(HaveOccurred())
		Expect(value).To(Equal("analytics.orders"))
	})
})
