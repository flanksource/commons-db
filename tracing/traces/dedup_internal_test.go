// Specs for the dedup window: a record whose key a session already emitted
// within the window is dropped, and keys outside it are forgotten.

package traces

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("deduplicator", func() {
	var now time.Time
	var dedup *deduplicator[string]

	BeforeEach(func() {
		now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
		dedup = newDeduplicator(time.Hour, func(s string) string { return s })
		dedup.now = func() time.Time { return now }
	})

	It("drops a key seen within the window and admits it once the window has passed", func() {
		Expect(dedup.admit("a")).To(BeTrue())
		Expect(dedup.admit("b")).To(BeTrue())
		now = now.Add(59 * time.Minute)
		Expect(dedup.admit("a")).To(BeFalse())
		now = now.Add(2 * time.Minute)
		Expect(dedup.admit("a")).To(BeTrue())
	})

	It("measures the window from the admitted record, not from a dropped one", func() {
		Expect(dedup.admit("a")).To(BeTrue())
		now = now.Add(50 * time.Minute)
		Expect(dedup.admit("a")).To(BeFalse())
		now = now.Add(11 * time.Minute)
		Expect(dedup.admit("a")).To(BeTrue())
	})

	It("forgets keys whose window has passed as it sweeps", func() {
		for _, key := range []string{"a", "b", "c"} {
			Expect(dedup.admit(key)).To(BeTrue())
		}
		now = now.Add(2 * time.Hour)
		dedup.sweep()
		Expect(dedup.seen).To(BeEmpty())
	})
})
