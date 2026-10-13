// Specs for decoding a JFR recording into profile samples: every expectation
// was read from the fixture with the JDK's own jfr tool (testdata/README.md).

package jfr_test

import (
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/tracing/traces/jfr"
)

const (
	jdkProfile     = "jdk-profile"
	asyncCPULock   = "async-profiler-cpu-lock-alloc"
	asyncWallClock = "async-profiler-wall"
)

// sums adds up a column of samples by event, or by thread for one event.
func sums(samples []jfr.Sample, key func(jfr.Sample) string, value func(jfr.Sample) int64) map[string]int64 {
	totals := map[string]int64{}
	for _, sample := range samples {
		totals[key(sample)] += value(sample)
	}
	return totals
}

func byEvent(sample jfr.Sample) string { return sample.Event }
func count(sample jfr.Sample) int64    { return sample.Samples }
func weight(sample jfr.Sample) int64   { return sample.Value }

// ofEvent is the samples of one event.
func ofEvent(samples []jfr.Sample, event string) []jfr.Sample {
	var matched []jfr.Sample
	for _, sample := range samples {
		if sample.Event == event {
			matched = append(matched, sample)
		}
	}
	return matched
}

func decode(name string, options jfr.DecodeOptions) jfr.Profile {
	profile, err := jfr.Decode(fixture(name), options)
	Expect(err).ToNot(HaveOccurred())
	return profile
}

var _ = Describe("Decode", func() {
	DescribeTable("counts every sample of the events it decodes, up to the latest event's end",
		func(name string, counts map[string]int64, latest time.Time) {
			profile := decode(name, jfr.DecodeOptions{})
			Expect(profile.Counts).To(Equal(counts))
			Expect(sums(profile.Samples, byEvent, count)).To(Equal(counts), "the samples add up to the counts")
			Expect(profile.Latest).To(BeTemporally("~", latest, time.Microsecond))
		},
		Entry("a JDK recording", jdkProfile,
			map[string]int64{jfr.EventCPU: 172, jfr.EventAlloc: 720, jfr.EventLock: 67, jfr.EventPark: 72},
			time.Date(2026, 10, 13, 11, 6, 14, 956490708, time.UTC)),
		Entry("an async-profiler cpu recording with allocations and locks", asyncCPULock,
			map[string]int64{jfr.EventCPU: 582, jfr.EventAlloc: 7972 + 2238, jfr.EventLock: 1773, jfr.EventPark: 435},
			time.Date(2023, 8, 3, 4, 53, 13, 681970365, time.UTC)),
		Entry("an async-profiler wall-clock recording, whose events each carry several samples", asyncWallClock,
			map[string]int64{jfr.EventWall: 26246},
			time.Date(2026, 2, 19, 7, 47, 48, 861455921, time.UTC)),
	)

	DescribeTable("weighs each event in its own unit",
		func(name, event, unit string, total, tolerance int64) {
			samples := ofEvent(decode(name, jfr.DecodeOptions{}).Samples, event)
			Expect(samples).ToNot(BeEmpty())
			for _, sample := range samples {
				Expect(sample.Unit).To(Equal(unit))
			}
			Expect(sums(samples, byEvent, weight)[event]).To(BeNumerically("~", total, tolerance))
		},
		Entry("cpu in samples", jdkProfile, jfr.EventCPU, jfr.UnitSamples, int64(172), int64(0)),
		Entry("sampled allocations in bytes", jdkProfile, jfr.EventAlloc, jfr.UnitBytes, int64(71607711184), int64(0)),
		// Durations are converted from ticks event by event, so each may round
		// by a nanosecond.
		Entry("monitor waits in nanoseconds", jdkProfile, jfr.EventLock, jfr.UnitNanos, int64(1943636419), int64(67)),
		Entry("parks in nanoseconds", jdkProfile, jfr.EventPark, jfr.UnitNanos, int64(2223843916), int64(72)),
		Entry("TLAB and outside-TLAB allocations in bytes", asyncCPULock, jfr.EventAlloc, jfr.UnitBytes, int64(1598335640+43715768), int64(0)),
		Entry("async-profiler monitor waits in nanoseconds", asyncCPULock, jfr.EventLock, jfr.UnitNanos, int64(1395726533), int64(1773)),
		Entry("async-profiler parks in nanoseconds", asyncCPULock, jfr.EventPark, jfr.UnitNanos, int64(36035783), int64(435)),
		Entry("wall-clock in samples", asyncWallClock, jfr.EventWall, jfr.UnitSamples, int64(26246), int64(0)),
	)

	It("collapses a stack root first into dotted frames, its leaf the top frame", func() {
		var burner []jfr.Sample
		for _, sample := range ofEvent(decode(jdkProfile, jfr.DecodeOptions{}).Samples, jfr.EventCPU) {
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
		samples := decode(jdkProfile, jfr.DecodeOptions{Threads: true}).Samples
		byThread := func(sample jfr.Sample) string { return sample.Thread }
		Expect(sums(ofEvent(samples, jfr.EventLock), byThread, count)).To(Equal(map[string]int64{"contender-1": 33, "contender-2": 34}))
		Expect(sums(ofEvent(samples, jfr.EventPark), byThread, count)).To(Equal(map[string]int64{"parker": 71, "Common-Cleaner": 1}))
		Expect(sums(ofEvent(samples, jfr.EventCPU), byThread, count)).To(HaveKeyWithValue("burner", int64(165)))
	})

	It("keeps a stack's leaf-most frames when it is deeper than the maximum, marking the cut", func() {
		samples := decode(jdkProfile, jfr.DecodeOptions{MaxStackDepth: 1}).Samples
		locks := ofEvent(samples, jfr.EventLock)
		Expect(locks).To(HaveLen(1), "stacks that differ only beyond the maximum depth are one stack")
		Expect(locks[0].Stack).To(Equal("[truncated];jfrfixture.Workload.contend"))
		Expect(locks[0].Frame).To(Equal("jfrfixture.Workload.contend"))
		Expect(locks[0].Samples).To(Equal(int64(67)))
	})

	DescribeTable("decodes only the events that end after the watermark",
		func(name string, after time.Time, counts map[string]int64) {
			profile := decode(name, jfr.DecodeOptions{After: after})
			Expect(profile.Counts).To(Equal(counts))
			for _, sample := range profile.Samples {
				Expect(sample.From).To(BeTemporally(">", after))
				Expect(sample.Timestamp).To(Equal(profile.Latest))
				Expect(sample.From).To(BeTemporally("<=", sample.Timestamp))
			}
			Expect(decode(name, jfr.DecodeOptions{After: profile.Latest}).Samples).To(BeEmpty(), "nothing ends after the latest event")
		},
		Entry("a JDK recording", jdkProfile, time.Date(2026, 10, 13, 11, 6, 14, 0, time.UTC),
			map[string]int64{jfr.EventCPU: 81, jfr.EventAlloc: 300, jfr.EventLock: 32, jfr.EventPark: 34}),
		Entry("an async-profiler recording", asyncCPULock, time.Date(2023, 8, 3, 4, 53, 8, 0, time.UTC),
			map[string]int64{jfr.EventCPU: 305, jfr.EventAlloc: 3878 + 962, jfr.EventLock: 552, jfr.EventPark: 232}),
		Entry("an async-profiler wall-clock recording", asyncWallClock, time.Date(2026, 2, 19, 7, 47, 44, 0, time.UTC),
			map[string]int64{jfr.EventWall: 3358}),
	)

	It("decodes a recording of several chunks, each resolving stacks through its own constant pool", func() {
		combined := append(fixture(asyncCPULock), fixture(jdkProfile)...)
		profile, err := jfr.Decode(combined, jfr.DecodeOptions{})
		Expect(err).ToNot(HaveOccurred())
		Expect(profile.Counts).To(Equal(map[string]int64{
			jfr.EventCPU: 582 + 172, jfr.EventAlloc: 10210 + 720, jfr.EventLock: 1773 + 67, jfr.EventPark: 435 + 72,
		}))
		Expect(ofEvent(profile.Samples, jfr.EventCPU)).To(ContainElement(HaveField("Frame", "jfrfixture.Workload.burn")))
	})

	DescribeTable("refuses what is not a whole JFR recording",
		func(data func() []byte, message string) {
			_, err := jfr.Decode(data(), jfr.DecodeOptions{})
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("nothing", func() []byte { return nil }, "empty"),
		Entry("a gzipped recording", func() []byte { return []byte("\x1f\x8b\x08\x00 compressed") }, "magic"),
		Entry("a recording cut short", func() []byte { data := fixture(jdkProfile); return data[:len(data)/2] }, "JFR"),
	)
})
