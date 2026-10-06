// Specs for the emitter's dedup admission: a key counts as emitted only once
// its record is accepted, so a dropped, unencodable or cancelled one is retried.

package traces

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/recordstore/probe"
)

type keyedRecord struct {
	Key   string `json:"key"`
	Extra any    `json:"extra,omitempty"`
}

func newKeyedEmitter(capacity int) (*emitter[keyedRecord], *source) {
	src := newSource("spec", capacity, "", false)
	return &emitter[keyedRecord]{
		source: src, process: pipeline(nil),
		dedup: newDeduplicator(time.Hour, func(r keyedRecord) string { return r.Key }),
	}, src
}

// commitAll stands in for the probe storing everything buffered.
func commitAll(src *source) {
	batch, err := src.Read(context.Background(), probe.Cursor{Next: src.base})
	Expect(err).ToNot(HaveOccurred())
	Expect(src.Commit(context.Background(), batch)).To(Succeed())
}

var _ = Describe("emitter deduplication", func() {
	It("admits a record whose earlier copy a full buffer dropped", func() {
		emitter, src := newKeyedEmitter(1)
		Expect(emitter.TryEmit(keyedRecord{Key: "a"})).To(BeTrue())
		Expect(emitter.TryEmit(keyedRecord{Key: "b"})).To(BeFalse())
		commitAll(src)
		Expect(emitter.TryEmit(keyedRecord{Key: "b"})).To(BeTrue())
		Expect(emitter.TryEmit(keyedRecord{Key: "a"})).To(BeFalse())
		Expect(src.summary).To(Equal(Summary{Emitted: 2, Dropped: 1, Deduplicated: 1}))
	})

	It("admits a record whose earlier copy did not encode", func() {
		emitter, _ := newKeyedEmitter(10)
		Expect(emitter.Emit(context.Background(), keyedRecord{Key: "a", Extra: make(chan int)})).ToNot(Succeed())
		Expect(emitter.Emit(context.Background(), keyedRecord{Key: "a"})).To(Succeed())
		Expect(len(emitter.source.rows)).To(Equal(1))
	})

	It("admits a record whose earlier emission was cancelled while it waited", func() {
		emitter, src := newKeyedEmitter(1)
		Expect(emitter.Emit(context.Background(), keyedRecord{Key: "a"})).To(Succeed())
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		Expect(emitter.Emit(cancelled, keyedRecord{Key: "b"})).To(MatchError(context.Canceled))
		commitAll(src)
		Expect(emitter.Emit(context.Background(), keyedRecord{Key: "b"})).To(Succeed())
		Expect(src.summary).To(Equal(Summary{Emitted: 2}))
	})
})

var _ = Describe("source as its capture stops", func() {
	It("holds no more than one final drain can commit, dropping and counting the rest", func() {
		src := newSource("spec", 2, "", false)
		src.drainLimit = 3
		src.prepare(dbcontext.New(), func(ctx dbcontext.Context) error {
			<-ctx.Done()
			for range 5 {
				if err := src.push(context.Background(), map[string]any{"n": 1}); err != nil {
					return err
				}
			}
			return nil
		}, nil)
		src.start()
		Expect(src.Freeze(context.Background())).To(Succeed())
		Expect(src.handlerErr()).ToNot(HaveOccurred())
		Expect(src.rows).To(HaveLen(3))
		Expect(src.summary).To(Equal(Summary{Emitted: 3, Dropped: 2}))
	})
})
