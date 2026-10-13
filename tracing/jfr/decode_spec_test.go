// Specs for decoding a JFR recording into profile samples: every expectation
// was read from the fixture with the JDK's own jfr tool (testdata/README.md).

package jfr_test

import (
	"strings"
	"time"

	"github.com/flanksource/commons-db/tracing/jfr/decode"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	jdkProfile     = "jdk-profile"
	asyncCPULock   = "async-profiler-cpu-lock-alloc"
	asyncWallClock = "async-profiler-wall"
)

// sums adds up a column of samples by event, or by thread for one event.
func sums(samples []decode.StackSample, key func(decode.StackSample) string, value func(decode.StackSample) int64) map[string]int64 {
	totals := map[string]int64{}
	for _, sample := range samples {
		totals[key(sample)] += value(sample)
	}
	return totals
}

func byEvent(sample decode.StackSample) string { return sample.Event }
func count(sample decode.StackSample) int64    { return sample.Samples }
func weight(sample decode.StackSample) int64   { return sample.Value }

// ofEvent is the samples of one event.
func ofEvent(samples []decode.StackSample, event string) []decode.StackSample {
	var matched []decode.StackSample
	for _, sample := range samples {
		if sample.Event == event {
			matched = append(matched, sample)
		}
	}
	return matched
}

func decodeFixture(name string, options decode.DecodeOptions) decode.Profile {
	profile, err := decode.Decode(fixture(name), options)
	Expect(err).ToNot(HaveOccurred())
	return profile
}

var _ = Describe("Decode", func() {
	DescribeTable("counts every sample of the events it decodes, up to the latest event's end",
		func(name string, counts map[string]int64, latest time.Time) {
			profile := decodeFixture(name, decode.DecodeOptions{})
			Expect(profile.Counts).To(Equal(counts))
			Expect(sums(profile.Samples, byEvent, count)).To(Equal(counts), "the samples add up to the counts")
			Expect(profile.Latest).To(BeTemporally("~", latest, time.Microsecond))
		},
		Entry("a JDK recording", jdkProfile,
			map[string]int64{decode.EventCPU: 172, decode.EventAlloc: 720, decode.EventLock: 67, decode.EventPark: 72},
			time.Date(2026, 10, 13, 11, 6, 14, 956490708, time.UTC)),
		Entry("an async-profiler cpu recording with allocations and locks", asyncCPULock,
			map[string]int64{decode.EventCPU: 582, decode.EventAlloc: 7972 + 2238, decode.EventLock: 1773, decode.EventPark: 435},
			time.Date(2023, 8, 3, 4, 53, 13, 681970365, time.UTC)),
		Entry("an async-profiler wall-clock recording, whose events each carry several samples", asyncWallClock,
			map[string]int64{decode.EventWall: 26246},
			time.Date(2026, 2, 19, 7, 47, 48, 861455921, time.UTC)),
	)

	DescribeTable("weighs each event in its own unit",
		func(name, event, unit string, total, tolerance int64) {
			samples := ofEvent(decodeFixture(name, decode.DecodeOptions{}).Samples, event)
			Expect(samples).ToNot(BeEmpty())
			for _, sample := range samples {
				Expect(sample.Unit).To(Equal(unit))
			}
			Expect(sums(samples, byEvent, weight)[event]).To(BeNumerically("~", total, tolerance))
		},
		Entry("cpu in samples", jdkProfile, decode.EventCPU, decode.UnitSamples, int64(172), int64(0)),
		Entry("sampled allocations in bytes", jdkProfile, decode.EventAlloc, decode.UnitBytes, int64(71607711184), int64(0)),
		// Durations are converted from ticks event by event, so each may round
		// by a nanosecond.
		Entry("monitor waits in nanoseconds", jdkProfile, decode.EventLock, decode.UnitNanos, int64(1943636419), int64(67)),
		Entry("parks in nanoseconds", jdkProfile, decode.EventPark, decode.UnitNanos, int64(2223843916), int64(72)),
		Entry("TLAB and outside-TLAB allocations in bytes", asyncCPULock, decode.EventAlloc, decode.UnitBytes, int64(1598335640+43715768), int64(0)),
		Entry("async-profiler monitor waits in nanoseconds", asyncCPULock, decode.EventLock, decode.UnitNanos, int64(1395726533), int64(1773)),
		Entry("async-profiler parks in nanoseconds", asyncCPULock, decode.EventPark, decode.UnitNanos, int64(36035783), int64(435)),
		Entry("wall-clock in samples", asyncWallClock, decode.EventWall, decode.UnitSamples, int64(26246), int64(0)),
	)

	It("collapses a stack root first into dotted frames, its leaf the top frame", func() {
		var burner []decode.StackSample
		for _, sample := range ofEvent(decodeFixture(jdkProfile, decode.DecodeOptions{}).Samples, decode.EventCPU) {
			Expect(sample.Thread).To(BeEmpty(), "samples are not split by thread unless asked")
			if sample.Frame == "jfrfixture.Workload.burn" {
				burner = append(burner, sample)
			}
		}
		Expect(burner).To(HaveLen(1))
		Expect(burner[0].Samples).To(Equal(int64(165)))
		frames := strings.Split(burner[0].Stack, ";")
		Expect(frames).To(HaveLen(4))
		Expect(frames[0]).To(Equal("java.lang.Thread.run"))
		Expect(frames[1]).To(Equal("java.lang.Thread.runWith"))
		Expect(frames[2]).To(And(HavePrefix("jfrfixture.Workload$$Lambda"), HaveSuffix(".run")))
		Expect(frames[3]).To(Equal("jfrfixture.Workload.burn"))
	})

	It("splits samples by thread when asked", func() {
		samples := decodeFixture(jdkProfile, decode.DecodeOptions{Threads: true}).Samples
		byThread := func(sample decode.StackSample) string { return sample.Thread }
		Expect(sums(ofEvent(samples, decode.EventLock), byThread, count)).To(Equal(map[string]int64{"contender-1": 33, "contender-2": 34}))
		Expect(sums(ofEvent(samples, decode.EventPark), byThread, count)).To(Equal(map[string]int64{"parker": 71, "Common-Cleaner": 1}))
		Expect(sums(ofEvent(samples, decode.EventCPU), byThread, count)).To(HaveKeyWithValue("burner", int64(165)))
	})

	It("keeps a stack's leaf-most frames when it is deeper than the maximum, marking the cut", func() {
		samples := decodeFixture(jdkProfile, decode.DecodeOptions{MaxStackDepth: 1}).Samples
		locks := ofEvent(samples, decode.EventLock)
		Expect(locks).To(HaveLen(1), "stacks that differ only beyond the maximum depth are one stack")
		Expect(locks[0].Stack).To(Equal("[truncated];jfrfixture.Workload.contend"))
		Expect(locks[0].Frame).To(Equal("jfrfixture.Workload.contend"))
		Expect(locks[0].Samples).To(Equal(int64(67)))
	})

	DescribeTable("decodes only the events that end after the watermark",
		func(name string, after time.Time, counts map[string]int64) {
			profile := decodeFixture(name, decode.DecodeOptions{After: after})
			Expect(profile.Counts).To(Equal(counts))
			for _, sample := range profile.Samples {
				Expect(sample.From).To(BeTemporally(">", after))
				Expect(sample.Timestamp).To(Equal(profile.Latest))
				Expect(sample.From).To(BeTemporally("<=", sample.Timestamp))
			}
			Expect(decodeFixture(name, decode.DecodeOptions{After: profile.Latest}).Samples).To(BeEmpty(), "nothing ends after the latest event")
		},
		Entry("a JDK recording", jdkProfile, time.Date(2026, 10, 13, 11, 6, 14, 0, time.UTC),
			map[string]int64{decode.EventCPU: 81, decode.EventAlloc: 300, decode.EventLock: 32, decode.EventPark: 34}),
		Entry("an async-profiler recording", asyncCPULock, time.Date(2023, 8, 3, 4, 53, 8, 0, time.UTC),
			map[string]int64{decode.EventCPU: 305, decode.EventAlloc: 3878 + 962, decode.EventLock: 552, decode.EventPark: 232}),
		Entry("an async-profiler wall-clock recording", asyncWallClock, time.Date(2026, 2, 19, 7, 47, 44, 0, time.UTC),
			map[string]int64{decode.EventWall: 3358}),
	)

	It("decodes a recording of several chunks, each resolving stacks through its own constant pool", func() {
		combined := append(fixture(asyncCPULock), fixture(jdkProfile)...)
		profile, err := decode.Decode(combined, decode.DecodeOptions{})
		Expect(err).ToNot(HaveOccurred())
		Expect(profile.Counts).To(Equal(map[string]int64{
			decode.EventCPU: 582 + 172, decode.EventAlloc: 10210 + 720, decode.EventLock: 1773 + 67, decode.EventPark: 435 + 72,
		}))
		Expect(ofEvent(profile.Samples, decode.EventCPU)).To(ContainElement(HaveField("Frame", "jfrfixture.Workload.burn")))
	})

	DescribeTable("refuses what is not a whole JFR recording",
		func(data func() []byte, message string) {
			_, err := decode.Decode(data(), decode.DecodeOptions{})
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("nothing", func() []byte { return nil }, "empty"),
		Entry("a gzipped recording", func() []byte { return []byte("\x1f\x8b\x08\x00 compressed") }, "magic"),
		Entry("a recording cut short", func() []byte { data := fixture(jdkProfile); return data[:len(data)/2] }, "JFR"),
	)
})
