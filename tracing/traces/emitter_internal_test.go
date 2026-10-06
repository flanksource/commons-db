// Specs for the emitter's dedup admission and the source's buffer: a key counts
// only once its record is accepted, and the buffer is bounded by rows and bytes.

package traces

import (
	"context"
	"strings"
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
	src := newSource("spec", capacity, defaultBufferBytes, "", false)
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
		src := newSource("spec", 2, defaultBufferBytes, "", false)
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

// sized is a row of about n bytes.
func sized(n int) map[string]any { return map[string]any{"s": strings.Repeat("x", n-1)} }

var _ = Describe("source holding rows by size", func() {
	It("waits for a commit once the rows it holds reach its byte cap", func() {
		src := newSource("spec", 10, 100, "", false)
		Expect(src.push(context.Background(), sized(60))).To(Succeed())
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		Expect(src.push(cancelled, sized(60))).To(MatchError(context.Canceled))
		commitAll(src)
		Expect(src.push(context.Background(), sized(60))).To(Succeed())
	})

	It("drops and counts a row that would take it past its byte cap", func() {
		src := newSource("spec", 10, 100, "", false)
		Expect(src.tryPush(sized(60))).To(BeTrue())
		Expect(src.tryPush(sized(60))).To(BeFalse())
		Expect(src.tryPush(sized(30))).To(BeTrue())
		Expect(src.summary).To(Equal(Summary{Emitted: 2, Dropped: 1}))
	})

	It("takes a row larger than its byte cap while it holds nothing", func() {
		src := newSource("spec", 10, 100, "", false)
		Expect(src.push(context.Background(), sized(500))).To(Succeed())
		Expect(src.tryPush(sized(10))).To(BeFalse())
		commitAll(src)
		Expect(src.tryPush(sized(500))).To(BeTrue())
	})

	It("drops rows past its byte cap as its capture stops", func() {
		src := newSource("spec", 10, 100, "", false)
		src.prepare(dbcontext.New(), func(ctx dbcontext.Context) error {
			<-ctx.Done()
			for range 3 {
				if err := src.push(context.Background(), sized(40)); err != nil {
					return err
				}
			}
			return nil
		}, nil)
		src.start()
		Expect(src.Freeze(context.Background())).To(Succeed())
		Expect(src.rows).To(HaveLen(2))
		Expect(src.summary).To(Equal(Summary{Emitted: 2, Dropped: 1}))
	})

	It("measures a row by its keys and text, and every other value as eight bytes", func() {
		Expect(rowBytes(map[string]any{
			"ab": "xyz", "n": 1.5, "list": []any{"q", true, nil}, "nested": map[string]any{"k": "vv"},
		})).To(Equal(2 + 3 + 1 + 8 + 4 + 1 + 8 + 8 + 6 + 1 + 2))
	})

	It("holds 64MiB unless its runtime says otherwise", func() {
		Expect((&Runtime{}).bufferBytes()).To(Equal(64 << 20))
		Expect((&Runtime{BufferBytes: 1024}).bufferBytes()).To(Equal(1024))
	})
})

var _ = Describe("source collapsing a repeated key", func() {
	It("counts only the copies each committed page left out", func() {
		src := newSource("spec", 10, defaultBufferBytes, "label", false)
		for range 2 {
			Expect(src.push(context.Background(), map[string]any{"label": "same"})).To(Succeed())
			Expect(src.push(context.Background(), map[string]any{"label": "same"})).To(Succeed())
			batch, err := src.Read(context.Background(), probe.Cursor{Next: src.base})
			Expect(err).ToNot(HaveOccurred())
			Expect(batch.Rows).To(HaveLen(1))
			Expect(src.Commit(context.Background(), batch)).To(Succeed())
		}
		// The second page's copy repeats the first's: the store skips it.
		Expect(src.summary).To(Equal(Summary{Emitted: 4, Collapsed: 2}))
	})
})
