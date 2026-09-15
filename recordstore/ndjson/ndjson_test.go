package ndjson_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/ndjson"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
)

func TestNDJSON(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Record Store NDJSON Suite")
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func openNDJSON(dir string, clock *fakeClock, maxBytes int64, keep int) *ndjson.Backend {
	backend, err := ndjson.New(ndjson.Options{
		Dir: dir, Schema: recordstoretest.Schema, MaxBytes: maxBytes, KeepStreams: keep, TTL: recordstoretest.TTL, Now: clock.Now,
	})
	Expect(err).ToNot(HaveOccurred())
	return backend
}

var _ = Describe("ndjson backend", func() {
	recordstoretest.Conformance(func() recordstoretest.Harness {
		clock := &fakeClock{now: time.Now()}
		dir := GinkgoT().TempDir()
		open := func() recordstore.Backend { return openNDJSON(dir, clock, 1<<20, 100) }
		return recordstoretest.Harness{
			Backend: open(), Elapse: clock.Advance, Advance: clock.Advance, Now: clock.Now, Reopen: open,
		}
	})
})

var _ = Describe("ndjson backend files", func() {
	var (
		ctx   context.Context
		dir   string
		clock *fakeClock
	)

	BeforeEach(func() {
		ctx = context.Background()
		dir = GinkgoT().TempDir()
		clock = &fakeClock{now: time.Now()}
	})

	It("writes one line per row, seq and row, to <dir>/<kind>/<stream>.ndjson", func() {
		backend := openNDJSON(dir, clock, 1<<20, 10)
		_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, []recordstore.Row{{"name": "a"}, {"name": "b"}})
		Expect(err).ToNot(HaveOccurred())

		content, err := os.ReadFile(filepath.Join(dir, recordstoretest.Kind, "run-1.ndjson"))
		Expect(err).ToNot(HaveOccurred())
		Expect(string(content)).To(Equal("{\"seq\":1,\"row\":{\"name\":\"a\"}}\n{\"seq\":2,\"row\":{\"name\":\"b\"}}\n"))
	})

	It("refuses an append that would take the file past its cap, whole and loudly, and marks the stream capped", func() {
		backend := openNDJSON(dir, clock, 400, 10)
		_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 2))
		Expect(err).ToNot(HaveOccurred())

		_, err = backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(3, 6))
		Expect(errors.Is(err, recordstore.ErrCapacity)).To(BeTrue(), "%v", err)

		meta, err := backend.Meta(ctx, "run-1")
		Expect(err).ToNot(HaveOccurred())
		Expect(meta.Capped).To(BeTrue())
		Expect(meta.HighSeq).To(Equal(int64(2)))
		info, err := os.Stat(filepath.Join(dir, recordstoretest.Kind, "run-1.ndjson"))
		Expect(err).ToNot(HaveOccurred())
		Expect(info.Size()).To(BeNumerically("<=", 400))
		seqs, _ := recordstoretest.Scanned(backend, "run-1", 0)
		Expect(seqs).To(Equal([]int64{1, 2}))
	})

	It("keeps only the newest streams of a kind when a new one opens", func() {
		backend := openNDJSON(dir, clock, 1<<20, 2)
		for _, stream := range []string{"run-1", "run-2", "run-3"} {
			clock.now = clock.now.Add(time.Minute)
			_, err := backend.Append(ctx, stream, recordstoretest.Kind, recordstoretest.SampleRows(1, 1))
			Expect(err).ToNot(HaveOccurred())
		}

		_, err := backend.Meta(ctx, "run-1")
		Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue(), "%v", err)
		Expect(filepath.Join(dir, recordstoretest.Kind, "run-1.ndjson")).ToNot(BeAnExistingFile())
		for _, stream := range []string{"run-2", "run-3"} {
			_, err := backend.Meta(ctx, stream)
			Expect(err).ToNot(HaveOccurred())
		}
	})

	It("drops a tail written past the last committed append before appending again", func() {
		backend := openNDJSON(dir, clock, 1<<20, 10)
		_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 1))
		Expect(err).ToNot(HaveOccurred())
		file, err := os.OpenFile(filepath.Join(dir, recordstoretest.Kind, "run-1.ndjson"), os.O_APPEND|os.O_WRONLY, 0)
		Expect(err).ToNot(HaveOccurred())
		_, err = file.WriteString(`{"seq":2,"row":{"name":"orphan"}}` + "\n")
		Expect(err).ToNot(HaveOccurred())
		Expect(file.Close()).To(Succeed())

		_, err = backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(2, 2))
		Expect(err).ToNot(HaveOccurred())
		_, rows := recordstoretest.Scanned(backend, "run-1", 0)
		Expect(recordstoretest.Normalize(rows)).To(Equal(recordstoretest.Normalize(recordstoretest.SampleRows(1, 2))))
	})

	Describe("an incremental scan", func() {
		// variedRows differ in length, so no line's offset can be computed
		// from its seq.
		variedRows := func(first, last int) []recordstore.Row {
			rows := make([]recordstore.Row, 0, last-first+1)
			for n := first; n <= last; n++ {
				rows = append(rows, recordstore.Row{"name": strings.Repeat("x", n%17), "n": n})
			}
			return rows
		}

		var backend *ndjson.Backend

		BeforeEach(func() {
			backend = openNDJSON(dir, clock, 1<<20, 10)
			for first := 1; first <= 100; first += 25 {
				_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, variedRows(first, first+24))
				Expect(err).ToNot(HaveOccurred())
			}
		})

		DescribeTable("reads exactly the rows after the seq it is given",
			func(after int) {
				seqs, rows := recordstoretest.Scanned(backend, "run-1", int64(after))
				expected := variedRows(after+1, 100)
				Expect(seqs).To(HaveLen(len(expected)))
				Expect(seqs[0]).To(Equal(int64(after + 1)))
				Expect(recordstoretest.Normalize(rows)).To(Equal(recordstoretest.Normalize(expected)))
			},
			Entry("the first seq", 1),
			Entry("an append boundary", 25),
			Entry("mid-append", 63),
			Entry("the seq before the last", 99),
		)

		DescribeTable("reads nothing after the last seq",
			func(after int64) {
				seqs, _ := recordstoretest.Scanned(backend, "run-1", after)
				Expect(seqs).To(BeEmpty())
			},
			Entry("the last seq", int64(100)),
			Entry("past the last seq", int64(150)),
		)

		// A line before the resume point is never parsed: a scan that did would
		// trip over it, which is what re-reading from seq 1 costs on every
		// incremental Ensure.
		It("starts at the committed line after the seq rather than parsing the file from seq 1", func() {
			path := filepath.Join(dir, recordstoretest.Kind, "run-1.ndjson")
			content, err := os.ReadFile(path)
			Expect(err).ToNot(HaveOccurred())
			second := strings.Index(string(content), `{"seq":2,`)
			Expect(second).To(BeNumerically(">", 0))
			corrupted := strings.Index(string(content[second:]), `"row":{`) + second + len(`"row":{`)
			content[corrupted] = '#'
			Expect(os.WriteFile(path, content, 0o600)).To(Succeed())

			seqs, _ := recordstoretest.Scanned(backend, "run-1", 90)
			Expect(seqs).To(Equal([]int64{91, 92, 93, 94, 95, 96, 97, 98, 99, 100}))
			err = backend.Scan(ctx, "run-1", 0, func(int64, recordstore.Row) error { return nil })
			Expect(err).To(MatchError(ContainSubstring("seq 2")))
		})
	})

	It("reports a stream file that vanished from under its metadata", func() {
		backend := openNDJSON(dir, clock, 1<<20, 10)
		_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 1))
		Expect(err).ToNot(HaveOccurred())
		Expect(os.Remove(filepath.Join(dir, recordstoretest.Kind, "run-1.ndjson"))).To(Succeed())

		err = backend.Scan(ctx, "run-1", 0, func(int64, recordstore.Row) error { return nil })
		Expect(err).To(HaveOccurred())
		Expect(strings.Contains(err.Error(), "run-1.ndjson")).To(BeTrue(), err.Error())
	})

	DescribeTable("refuses options it cannot run with",
		func(options ndjson.Options, message string) {
			_, err := ndjson.New(options)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("no directory", ndjson.Options{Schema: recordstoretest.Schema, MaxBytes: 1, KeepStreams: 1}, "directory"),
		Entry("no schema", ndjson.Options{Dir: "x", MaxBytes: 1, KeepStreams: 1}, "schema"),
		Entry("no cap", ndjson.Options{Dir: "x", Schema: recordstoretest.Schema, KeepStreams: 1}, "cap"),
		Entry("nothing kept", ndjson.Options{Dir: "x", Schema: recordstoretest.Schema, MaxBytes: 1}, "keep"),
	)

	It("moves a trimmed stream's rows to a file named by its low seq and removes the old one", func() {
		backend := openNDJSON(dir, clock, 1<<20, 10)
		_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 2))
		Expect(err).ToNot(HaveOccurred())
		clock.Advance(time.Minute)
		_, err = backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(3, 3))
		Expect(err).ToNot(HaveOccurred())

		_, err = backend.Trim(ctx, "run-1", clock.Now())
		Expect(err).ToNot(HaveOccurred())
		content, err := os.ReadFile(filepath.Join(dir, recordstoretest.Kind, "run-1@3.ndjson"))
		Expect(err).ToNot(HaveOccurred())
		Expect(strings.Count(string(content), "\n")).To(Equal(1))
		Expect(string(content)).To(HavePrefix(`{"seq":3,`))
		Expect(filepath.Join(dir, recordstoretest.Kind, "run-1.ndjson")).ToNot(BeAnExistingFile())
	})

	It("removes every data file of a stream it rotates out, and no other stream's", func() {
		backend := openNDJSON(dir, clock, 1<<20, 1)
		for _, stream := range []string{"run", "run.1"} {
			_, err := backend.Append(ctx, stream, recordstoretest.Kind, recordstoretest.SampleRows(1, 1))
			Expect(err).ToNot(HaveOccurred())
			clock.Advance(time.Minute)
		}
		_, err := backend.Meta(ctx, "run")
		Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue(), "%v", err)
		orphan := filepath.Join(dir, recordstoretest.Kind, "run.1@7.ndjson")
		Expect(os.WriteFile(orphan, nil, 0o600)).To(Succeed())

		_, err = backend.Append(ctx, "run-2", recordstoretest.Kind, recordstoretest.SampleRows(1, 1))
		Expect(err).ToNot(HaveOccurred())
		Expect(orphan).ToNot(BeAnExistingFile())
		Expect(filepath.Join(dir, recordstoretest.Kind, "run-2.ndjson")).To(BeAnExistingFile())
	})

	It("refuses a kind retaining rows when it keeps streams without a ttl", func() {
		backend, err := ndjson.New(ndjson.Options{Dir: dir, Schema: recordstoretest.Schema, MaxBytes: 1 << 20, KeepStreams: 1, Now: clock.Now})
		Expect(err).ToNot(HaveOccurred())
		_, err = backend.Append(ctx, "run-1", recordstoretest.RollingKind, recordstoretest.SampleRows(1, 1))
		Expect(err).To(MatchError(ContainSubstring("retains rows")))
	})
})
