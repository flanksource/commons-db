package process

import (
	"time"

	clickyprocess "github.com/flanksource/clicky/process"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("cpuRates", func() {
	const pid = 4242
	started := time.Date(2026, time.October, 1, 9, 0, 0, 0, time.UTC)
	at := started.Add(time.Hour)
	listing := func(cpu time.Duration, start time.Time) []clickyprocess.Process {
		return []clickyprocess.Process{{PID: pid, StartedAt: &start, CPUTime: cpu}}
	}
	key := Key(listing(0, started)[0])

	It("is unknown at the first observation and until a full window has passed", func() {
		rates := newCPURates()
		Expect(rates.observe(listing(0, started), at)).To(HaveKeyWithValue(key, BeNil()))
		Expect(rates.observe(listing(200*time.Millisecond, started), at.Add(250*time.Millisecond))).
			To(HaveKeyWithValue(key, BeNil()), "a 250ms gap is below the 1s window")
	})

	It("rates CPU over a window of at least 1s from the earlier baseline, carrying it between windows", func() {
		rates := newCPURates()
		_, err := rates.observe(listing(0, started), at)
		Expect(err).ToNot(HaveOccurred())
		_, err = rates.observe(listing(100*time.Millisecond, started), at.Add(250*time.Millisecond))
		Expect(err).ToNot(HaveOccurred())
		Expect(rates.observe(listing(1500*time.Millisecond, started), at.Add(time.Second))).
			To(HaveKeyWithValue(key, HaveValue(BeNumerically("~", 150, 1e-9))), "1.5s of CPU over the 1s since the first baseline")
		Expect(rates.observe(listing(1600*time.Millisecond, started), at.Add(1250*time.Millisecond))).
			To(HaveKeyWithValue(key, HaveValue(BeNumerically("~", 150, 1e-9))), "the last window's rate until the next window completes")
		Expect(rates.observe(listing(2*time.Second, started), at.Add(2*time.Second))).
			To(HaveKeyWithValue(key, HaveValue(BeNumerically("~", 50, 1e-9))), "0.5s of CPU over the 1s since the second baseline")
	})

	It("is unknown again for a process that reused the pid within the same second", func() {
		rates := newCPURates()
		_, err := rates.observe(listing(5*time.Second, started), at)
		Expect(err).ToNot(HaveOccurred())
		reused := started.Add(300 * time.Millisecond)
		Expect(Key(listing(0, reused)[0])).To(Equal(key), "same pid and start second share a key")
		Expect(rates.observe(listing(0, reused), at.Add(time.Second))).To(HaveKeyWithValue(key, BeNil()))
		Expect(rates.observe(listing(time.Second, reused), at.Add(2*time.Second))).
			To(HaveKeyWithValue(key, HaveValue(BeNumerically("~", 100, 1e-9))), "rated from the new process's first observation")
	})

	DescribeTable("is unknown for a process that has exited, whose CPU time ps may report as 0",
		func(status string) {
			rates := newCPURates()
			_, err := rates.observe(listing(2*time.Second, started), at)
			Expect(err).ToNot(HaveOccurred())
			gone := listing(0, started)
			gone[0].Status = status
			Expect(rates.observe(gone, at.Add(time.Second))).To(HaveKeyWithValue(key, BeNil()))
		},
		Entry("zombie", "zombie"),
		Entry("exiting", "exiting"),
	)

	It("keeps a process's CPU% when another capture observes the same listing", func() {
		rates := newCPURates()
		_, err := rates.observe(listing(0, started), at)
		Expect(err).ToNot(HaveOccurred())
		Expect(rates.observe(listing(time.Second, started), at.Add(time.Second))).To(HaveKeyWithValue(key, HaveValue(BeNumerically("~", 100, 1e-9))))
		Expect(rates.observe(listing(time.Second, started), at.Add(time.Second))).To(HaveKeyWithValue(key, HaveValue(BeNumerically("~", 100, 1e-9))))
	})

	It("is unknown again when ps reports the same start second but less CPU time, a pid reused within that second", func() {
		rates := newCPURates()
		_, err := rates.observe(listing(2*time.Second, started), at)
		Expect(err).ToNot(HaveOccurred())
		Expect(rates.observe(listing(110*time.Millisecond, started), at.Add(time.Second))).To(HaveKeyWithValue(key, BeNil()))
		Expect(rates.observe(listing(1110*time.Millisecond, started), at.Add(2*time.Second))).
			To(HaveKeyWithValue(key, HaveValue(BeNumerically("~", 100, 1e-9))), "rated from the new process's first observation")
	})
})

var _ = Describe("Node.update", func() {
	It("takes its peak CPU only from known readings", func() {
		known := func(percent float64) *float64 { return &percent }
		node := &Node{}
		node.update(observed{Process: clickyprocess.Process{PID: 1}}, "", time.Now())
		Expect(node.CPUPercent).To(BeNil())
		Expect(node.PeakCPUPercent).To(BeNil(), "an unknown reading is not a 0% peak")
		Expect(node.update(observed{Process: clickyprocess.Process{PID: 1}, cpuPercent: known(30)}, "", time.Now())).To(BeTrue())
		Expect(node.update(observed{Process: clickyprocess.Process{PID: 1}, cpuPercent: known(10)}, "", time.Now())).To(BeTrue())
		Expect(node.CPUPercent).To(HaveValue(Equal(10.0)))
		Expect(node.PeakCPUPercent).To(HaveValue(Equal(30.0)))
	})
})
