// Package recordstoretest is the conformance suite every recordstore.Backend
// runs, so the kv, sqlite and ndjson backends can never disagree about what
// appending, scanning, expiring and describing a stream mean.
package recordstoretest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

const (
	// Kind is the kind most conformance streams are written under: unkeyed,
	// kept whole until the stream expires.
	Kind = "sample"

	// KeyedKind holds each name once per stream.
	KeyedKind = "keyed"

	// RollingKind holds each name once and keeps each row TTL after its append.
	RollingKind = "rolling"
)

// ExpiryTTL is the ttl the expiry spec sets. It is short because the
// in-process kv store expires against the wall clock, so its Elapse sleeps.
const ExpiryTTL = 200 * time.Millisecond

// TTL is the stream ttl every conformance backend is opened with.
const TTL = time.Hour

// Columns is the columns of every conformance kind, for a backend that stores
// rows by column.
var Columns = []query.ColumnDef{
	{Name: "name", Type: query.ColumnTypeString},
	{Name: "count", Type: query.ColumnTypeNumber},
	{Name: "ok", Type: query.ColumnTypeBoolean},
	{Name: "detail", Type: query.ColumnTypeJSON},
}

var schemas = func() *recordstore.Schemas {
	catalog := recordstore.NewSchemas()
	for kind, options := range map[string]recordstore.KindOptions{
		Kind:        {},
		KeyedKind:   {Key: "name"},
		RollingKind: {Key: "name", Retention: recordstore.RetainRows},
	} {
		if err := catalog.Register(kind, Columns, options); err != nil {
			panic(err)
		}
	}
	return catalog
}()

// Schema resolves the conformance kinds and refuses every other kind.
func Schema(kind string) (recordstore.KindSchema, error) {
	return schemas.Kind(kind)
}

// Harness opens the backend under test.
type Harness struct {
	// Backend is a fresh, empty backend, opened with Schema and TTL.
	Backend recordstore.Backend

	// Elapse moves the backend's clock forward by d and reaps whatever that
	// expired, so a stream whose ttl is shorter than d is gone afterwards.
	Elapse func(d time.Duration)

	// Advance moves the backend's clock forward by d and reaps nothing, so
	// what a spec sees after it is only what the backend itself decided.
	Advance func(d time.Duration)

	// Now reads the backend's clock.
	Now func() time.Time

	// Reopen opens another backend over the storage Backend wrote, sharing
	// its clock, as a process restarting would.
	Reopen func() recordstore.Backend
}

// SampleRow is the n-th conformance row.
func SampleRow(n int) recordstore.Row {
	return recordstore.Row{
		"name": fmt.Sprintf("row-%03d", n), "count": n, "ok": n%2 == 0,
		"detail": map[string]any{"n": n, "tags": []any{"a", "b"}},
	}
}

// SampleRows are rows first..last.
func SampleRows(first, last int) []recordstore.Row {
	rows := make([]recordstore.Row, 0, last-first+1)
	for n := first; n <= last; n++ {
		rows = append(rows, SampleRow(n))
	}
	return rows
}

// Normalize is rows as their JSON encoding reads back, which is the only
// equality a row promises across backends: an int written may read back as a
// float, and a nested value as generic maps.
func Normalize(rows []recordstore.Row) []recordstore.Row {
	encoded, err := json.Marshal(rows)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	var decoded []recordstore.Row
	gomega.Expect(json.Unmarshal(encoded, &decoded)).To(gomega.Succeed())
	return decoded
}

// Scanned is every row a stream holds after afterSeq, keyed by seq.
func Scanned(backend recordstore.Backend, stream string, afterSeq int64) ([]int64, []recordstore.Row) {
	var seqs []int64
	var rows []recordstore.Row
	gomega.Expect(backend.Scan(context.Background(), stream, afterSeq, func(seq int64, row recordstore.Row) error {
		seqs = append(seqs, seq)
		rows = append(rows, row)
		return nil
	})).To(gomega.Succeed())
	return seqs, rows
}

// Conformance registers the suite. open is called before every spec.
func Conformance(open func() Harness) {
	s := &suite{}
	ginkgo.BeforeEach(func() {
		s.ctx = context.Background()
		s.harness = open()
		s.backend = s.harness.Backend
		ginkgo.DeferCleanup(func() { gomega.Expect(s.backend.Close()).To(gomega.Succeed()) })
	})
	s.appendSpecs()
	s.refusalSpecs()
	s.concurrencySpecs()
	s.expirySpecs()
	s.keySpecs()
	s.trimSpecs()
	s.retentionSpecs()
	s.tailSpecs()
}

// suite is the state every conformance spec reads, set before each one.
type suite struct {
	ctx     context.Context
	harness Harness
	backend recordstore.Backend
}

func (s *suite) appendRows(stream string, rows []recordstore.Row) recordstore.Window {
	return s.appendKind(stream, Kind, rows).Window
}

func (s *suite) appendKind(stream, kind string, rows []recordstore.Row) recordstore.AppendResult {
	result, err := s.backend.Append(s.ctx, stream, kind, rows)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	return result
}

func (s *suite) meta(stream string) recordstore.Meta {
	meta, err := s.backend.Meta(s.ctx, stream)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	return meta
}

// reopen replaces the backend with another over the same storage.
func (s *suite) reopen() {
	gomega.Expect(s.backend.Close()).To(gomega.Succeed())
	s.backend = s.harness.Reopen()
}

func (s *suite) appendSpecs() {
	ginkgo.It("numbers appended rows contiguously from 1 and reports each inclusive window", func() {
		gomega.Expect(s.appendRows("run-1", SampleRows(1, 3))).To(gomega.Equal(recordstore.Window{From: 1, To: 3}))
		gomega.Expect(s.appendRows("run-1", SampleRows(4, 5))).To(gomega.Equal(recordstore.Window{From: 4, To: 5}))

		seqs, rows := Scanned(s.backend, "run-1", 0)
		gomega.Expect(seqs).To(gomega.Equal([]int64{1, 2, 3, 4, 5}))
		gomega.Expect(Normalize(rows)).To(gomega.Equal(Normalize(SampleRows(1, 5))))
	})

	ginkgo.It("scans only the rows after the seq it is given", func() {
		s.appendRows("run-1", SampleRows(1, 4))
		s.appendRows("run-1", SampleRows(5, 7))

		seqs, rows := Scanned(s.backend, "run-1", 3)
		gomega.Expect(seqs).To(gomega.Equal([]int64{4, 5, 6, 7}))
		gomega.Expect(Normalize(rows)).To(gomega.Equal(Normalize(SampleRows(4, 7))))
		seqs, _ = Scanned(s.backend, "run-1", 7)
		gomega.Expect(seqs).To(gomega.BeEmpty())
	})

	ginkgo.It("describes a stream by generation, kind, count, high seq and last write", func() {
		before := time.Now().Add(-time.Second)
		s.appendRows("run-1", SampleRows(1, 3))

		meta, err := s.backend.Meta(s.ctx, "run-1")
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(meta.Generation).ToNot(gomega.BeEmpty())
		generation := meta.Generation
		s.appendRows("run-1", SampleRows(4, 4))
		updated, err := s.backend.Meta(s.ctx, "run-1")
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(updated.Generation).To(gomega.Equal(generation))
		gomega.Expect(meta.UpdatedAt).To(gomega.BeTemporally(">=", before))
		// A backend may give every stream a default lifetime; the expiry spec
		// below is what holds ExpiresAt to account.
		updated.UpdatedAt, updated.ExpiresAt = time.Time{}, nil
		gomega.Expect(updated).To(gomega.Equal(recordstore.Meta{
			Stream: "run-1", Kind: Kind, Total: 4, LowSeq: 1, HighSeq: 4, Generation: generation,
		}))
	})

	ginkgo.It("keeps streams apart", func() {
		s.appendRows("run-1", SampleRows(1, 2))
		gomega.Expect(s.appendRows("run-2", SampleRows(10, 10))).To(gomega.Equal(recordstore.Window{From: 1, To: 1}))

		_, rows := Scanned(s.backend, "run-2", 0)
		gomega.Expect(Normalize(rows)).To(gomega.Equal(Normalize(SampleRows(10, 10))))
	})

	ginkgo.It("creates an empty stream from an empty append, so a capture of nothing still reads as a stream", func() {
		gomega.Expect(s.appendRows("run-1", nil)).To(gomega.Equal(recordstore.Window{From: 1, To: 0}))

		meta, err := s.backend.Meta(s.ctx, "run-1")
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(meta.Total).To(gomega.BeZero())
		gomega.Expect(meta.Kind).To(gomega.Equal(Kind))
	})
}

func (s *suite) refusalSpecs() {
	ginkgo.It("reports an unknown stream as recordstore.ErrNotFound", func() {
		_, err := s.backend.Meta(s.ctx, "missing")
		gomega.Expect(errors.Is(err, recordstore.ErrNotFound)).To(gomega.BeTrue(), "Meta: %v", err)
		err = s.backend.Scan(s.ctx, "missing", 0, func(int64, recordstore.Row) error { return nil })
		gomega.Expect(errors.Is(err, recordstore.ErrNotFound)).To(gomega.BeTrue(), "Scan: %v", err)
		err = s.backend.Expire(s.ctx, "missing", time.Hour)
		gomega.Expect(errors.Is(err, recordstore.ErrNotFound)).To(gomega.BeTrue(), "Expire: %v", err)
		_, err = s.backend.Trim(s.ctx, "missing", time.Now())
		gomega.Expect(errors.Is(err, recordstore.ErrNotFound)).To(gomega.BeTrue(), "Trim: %v", err)
	})

	ginkgo.It("refuses an invalid stream id or kind before writing anything", func() {
		_, err := s.backend.Append(s.ctx, "run/1", Kind, SampleRows(1, 1))
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("stream id")))
		_, err = s.backend.Append(s.ctx, "run-1", "bad kind", SampleRows(1, 1))
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("kind")))
		_, err = s.backend.Meta(s.ctx, "run-1")
		gomega.Expect(errors.Is(err, recordstore.ErrNotFound)).To(gomega.BeTrue())
	})

	ginkgo.It("refuses to append a stream under a second kind", func() {
		s.appendRows("run-1", SampleRows(1, 1))
		_, err := s.backend.Append(s.ctx, "run-1", "other", SampleRows(2, 2))
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring(`kind "sample"`)))
	})

	ginkgo.It("stops a scan at the callback's error and returns it", func() {
		s.appendRows("run-1", SampleRows(1, 3))
		stop := errors.New("stop")
		var seen []int64
		err := s.backend.Scan(s.ctx, "run-1", 0, func(seq int64, _ recordstore.Row) error {
			seen = append(seen, seq)
			return stop
		})
		gomega.Expect(errors.Is(err, stop)).To(gomega.BeTrue())
		gomega.Expect(seen).To(gomega.Equal([]int64{1}))
	})
}

func (s *suite) concurrencySpecs() {
	ginkgo.It("serializes concurrent appends to one stream into distinct contiguous seqs", func() {
		const writers = 8
		var wg sync.WaitGroup
		for writer := range writers {
			wg.Add(1)
			go func() {
				defer ginkgo.GinkgoRecover()
				defer wg.Done()
				_, err := s.backend.Append(s.ctx, "run-1", Kind, SampleRows(writer*2+1, writer*2+2))
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
			}()
		}
		wg.Wait()

		seqs, _ := Scanned(s.backend, "run-1", 0)
		gomega.Expect(seqs).To(gomega.HaveLen(writers * 2))
		for index, seq := range seqs {
			gomega.Expect(seq).To(gomega.Equal(int64(index + 1)))
		}
	})
}

func (s *suite) expirySpecs() {
	ginkgo.It("expires a stream, rows written after the expiry included, once its ttl passes", func() {
		s.appendRows("run-1", SampleRows(1, 2))
		gomega.Expect(s.backend.Expire(s.ctx, "run-1", ExpiryTTL)).To(gomega.Succeed())
		s.appendRows("run-1", SampleRows(3, 3))

		meta, err := s.backend.Meta(s.ctx, "run-1")
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(meta.ExpiresAt).ToNot(gomega.BeNil())
		gomega.Expect(*meta.ExpiresAt).To(gomega.BeTemporally("~", time.Now().Add(ExpiryTTL), time.Second))

		s.harness.Elapse(2 * ExpiryTTL)
		_, err = s.backend.Meta(s.ctx, "run-1")
		gomega.Expect(errors.Is(err, recordstore.ErrNotFound)).To(gomega.BeTrue(), "Meta after expiry: %v", err)
		err = s.backend.Scan(s.ctx, "run-1", 0, func(int64, recordstore.Row) error { return nil })
		gomega.Expect(errors.Is(err, recordstore.ErrNotFound)).To(gomega.BeTrue(), "Scan after expiry: %v", err)
	})

	ginkgo.It("assigns a new generation when an expired stream id is reused", func() {
		s.appendRows("run-1", SampleRows(1, 1))
		original, err := s.backend.Meta(s.ctx, "run-1")
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(s.backend.Expire(s.ctx, "run-1", ExpiryTTL)).To(gomega.Succeed())
		s.harness.Elapse(2 * ExpiryTTL)

		s.appendRows("run-1", SampleRows(2, 2))
		recreated, err := s.backend.Meta(s.ctx, "run-1")
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(recreated.Generation).ToNot(gomega.BeEmpty())
		gomega.Expect(recreated.Generation).ToNot(gomega.Equal(original.Generation))
	})

	ginkgo.It("refuses an expiry that is not in the future", func() {
		s.appendRows("run-1", SampleRows(1, 1))
		gomega.Expect(s.backend.Expire(s.ctx, "run-1", 0)).To(gomega.MatchError(gomega.ContainSubstring("ttl")))
	})
}
