package devtools_test

import (
	"sync"

	"github.com/flanksource/commons-db/cmd/query/devtools"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type stamped struct {
	Sequence int64
	Name     string
}

func newStampedRing(max int) *devtools.Ring[stamped] {
	return devtools.NewRing(devtools.RingOptions[stamped]{
		Max:   max,
		Stamp: func(item *stamped, sequence int64) { item.Sequence = sequence },
	})
}

var _ = Describe("Ring", func() {
	It("stamps each item with a monotonic sequence starting at one", func() {
		ring := newStampedRing(4)
		Expect(ring.Append(stamped{Name: "a"})).To(Equal(int64(1)))
		Expect(ring.Append(stamped{Name: "b"})).To(Equal(int64(2)))
		Expect(ring.Items()).To(Equal([]stamped{{Sequence: 1, Name: "a"}, {Sequence: 2, Name: "b"}}))
	})

	It("evicts oldest first and counts what it dropped", func() {
		ring := newStampedRing(2)
		for _, name := range []string{"a", "b", "c", "d"} {
			ring.Append(stamped{Name: name})
		}
		Expect(ring.Items()).To(Equal([]stamped{{Sequence: 3, Name: "c"}, {Sequence: 4, Name: "d"}}))
		Expect(ring.Dropped()).To(Equal(int64(2)))
		Expect(ring.OldestSequence()).To(Equal(int64(3)))
	})

	Describe("resuming from a sequence the client already holds", func() {
		It("replays only what came after it", func() {
			ring := newStampedRing(8)
			for _, name := range []string{"a", "b", "c"} {
				ring.Append(stamped{Name: name})
			}
			replay, _, cancel := ring.SubscribeFrom(2)
			defer cancel()
			Expect(replay).To(Equal([]stamped{{Sequence: 3, Name: "c"}}))
		})

		It("replays everything the ring still holds when the client is further behind than the buffer", func() {
			ring := newStampedRing(2)
			for _, name := range []string{"a", "b", "c", "d"} {
				ring.Append(stamped{Name: name})
			}
			replay, _, cancel := ring.SubscribeFrom(1)
			defer cancel()
			Expect(replay).To(HaveLen(2), "what survives is offered; Dropped() says the rest is gone")
			Expect(replay[0].Sequence).To(Equal(int64(3)))
		})

		It("replays nothing when the client is already current", func() {
			ring := newStampedRing(8)
			ring.Append(stamped{Name: "a"})
			replay, _, cancel := ring.SubscribeFrom(1)
			defer cancel()
			Expect(replay).To(BeEmpty())
		})
	})

	// The property the whole stream rests on: an item appended while a client is
	// subscribing must arrive exactly once — never lost between the replay and
	// the live channel, never delivered through both.
	It("hands over from replay to live with no gap and no duplicate", func() {
		ring := newStampedRing(256)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				ring.Append(stamped{Name: string(rune('a' + i%26))})
			}
		}()

		replay, live, cancel := ring.SubscribeFrom(0)
		defer cancel()
		wg.Wait()

		seen := append([]stamped(nil), replay...)
		for len(seen) < 100 {
			seen = append(seen, <-live)
		}
		for i, item := range seen {
			Expect(item.Sequence).To(Equal(int64(i+1)), "sequence %d arrived out of order or twice", i+1)
		}
	})

	It("drops frames for a subscriber that stopped reading, not for the ring", func() {
		ring := newStampedRing(2048)
		_, _, cancel := ring.SubscribeFrom(0)
		defer cancel()

		for range 1000 {
			ring.Append(stamped{Name: "flood"})
		}
		Expect(ring.Items()).To(HaveLen(1000), "the ring is unaffected by a stalled reader")
	})

	It("stops delivering after cancel", func() {
		ring := newStampedRing(8)
		_, live, cancel := ring.SubscribeFrom(0)
		cancel()

		ring.Append(stamped{Name: "after"})
		_, open := <-live
		Expect(open).To(BeFalse())
	})

	It("keeps climbing sequences after a clear so a resuming client is not replayed stale ids", func() {
		ring := newStampedRing(8)
		ring.Append(stamped{Name: "a"})
		ring.Clear()

		Expect(ring.Items()).To(BeEmpty())
		Expect(ring.Append(stamped{Name: "b"})).To(Equal(int64(2)))
	})

	It("refuses a ring with no bound rather than growing without one", func() {
		Expect(func() { newStampedRing(0) }).To(Panic())
	})
})
