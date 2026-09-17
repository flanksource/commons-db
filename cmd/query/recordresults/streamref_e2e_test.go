package recordresults_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/recordresults"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

// appendedRef appends events 1..count to run-1 through results, as a capture
// does, and returns the metadata the ref must be built from.
func appendedRef(ctx context.Context, results *recordresults.Results, count int) recordstore.Meta {
	_, err := recordstore.AppendTyped(ctx, results.Backend, "run-1", "sample_event", sampleEvents(1, count))
	Expect(err).ToNot(HaveOccurred())
	meta, err := results.Backend.Meta(ctx, "run-1")
	Expect(err).ToNot(HaveOccurred())
	Expect(meta.ExpiresAt).ToNot(BeNil())
	return meta
}

func openLocal(backend recordstore.BackendKind) (*recordresults.Results, recordstore.Settings) {
	settings := localSettings(backend)
	return openResults(recordresults.OpenOptions{
		Prefix: "trace-results", ConnectionName: "index", Settings: settings, Register: registerSampleEvents,
	}), settings
}

var _ = Describe("Results.Ref", func() {
	ctx := context.Background()
	host, hostErr := os.Hostname()

	BeforeEach(func() { Expect(hostErr).ToNot(HaveOccurred()) })

	It("describes a local sqlite stream by its metadata, this host and the file it is in", func() {
		results, settings := openLocal(recordstore.BackendSQLite)
		meta := appendedRef(ctx, results, 30)

		ref, err := results.Ref(ctx, "run-1", 0, 0)
		Expect(err).ToNot(HaveOccurred())
		Expect(ref).To(Equal(recordresults.StreamRef{
			Stream: "run-1", Kind: "sample_event", Generation: meta.Generation, Low: 1, High: 30, From: 1, To: 30, Total: 30,
			ExpiresAt: meta.ExpiresAt,
			Store:     recordresults.StoreLocation{Backend: recordstore.BackendSQLite, Host: host, File: filepath.Join(settings.Dir, "v4", "records.sqlite")},
		}))
	})

	It("keeps the window a caller asks for inside the stream's bounds", func() {
		results, _ := openLocal(recordstore.BackendSQLite)
		appendedRef(ctx, results, 30)

		ref, err := results.Ref(ctx, "run-1", 5, 12)
		Expect(err).ToNot(HaveOccurred())
		Expect([]int64{ref.Low, ref.High, ref.From, ref.To}).To(Equal([]int64{1, 30, 5, 12}))
	})

	It("names the data file a local ndjson stream is in", func() {
		results, settings := openLocal(recordstore.BackendNDJSON)
		appendedRef(ctx, results, 3)

		ref, err := results.Ref(ctx, "run-1", 0, 0)
		Expect(err).ToNot(HaveOccurred())
		Expect(ref.Store).To(Equal(recordresults.StoreLocation{
			Backend: recordstore.BackendNDJSON, Host: host,
			File: filepath.Join(settings.Dir, "ndjson", "sample_event", "run-1.ndjson"),
		}))
	})

	It("reports a routed kv stream as kv, with no host or file it could be read from", func() {
		schemas := recordstore.NewSchemas()
		results := openResults(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: localSettings(""), Source: kvRouter(schemas),
			Schemas: schemas, Register: registerSampleEvents,
		})
		meta := appendedRef(forTenant("a"), results, 7)

		ref, err := results.Ref(forTenant("a"), "run-1", 0, 0)
		Expect(err).ToNot(HaveOccurred())
		Expect(ref).To(Equal(recordresults.StreamRef{
			Stream: "run-1", Kind: "sample_event", Generation: meta.Generation, Low: 1, High: 7, From: 1, To: 7, Total: 7,
			ExpiresAt: meta.ExpiresAt, Store: recordresults.StoreLocation{Backend: recordstore.BackendKV},
		}))
	})

	It("reports the file of the sqlite store a route opened, not a location it guessed", func() {
		schemas := recordstore.NewSchemas()
		dir := GinkgoT().TempDir()
		router, err := recordstore.NewRouter(recordstore.RouterOptions{
			Route: tenantOf,
			Open: func(_ context.Context, route string) (recordstore.Backend, error) {
				return sqlite.Open(sqlite.Options{
					Path: filepath.Join(dir, route+".sqlite"), Schema: schemas.Kind, TTL: time.Hour, SweepInterval: time.Hour,
				})
			},
		})
		Expect(err).ToNot(HaveOccurred())
		results := openResults(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: localSettings(""), Source: router,
			Schemas: schemas, Register: registerSampleEvents,
		})
		appendedRef(forTenant("b"), results, 2)

		ref, err := results.Ref(forTenant("b"), "run-1", 0, 0)
		Expect(err).ToNot(HaveOccurred())
		Expect(ref.Store).To(Equal(recordresults.StoreLocation{
			Backend: recordstore.BackendSQLite, Host: host, File: filepath.Join(dir, "v4", "b.sqlite"),
		}))
	})

	DescribeTable("refuses a window the stream does not hold",
		func(from, to int64, message string) {
			results, _ := openLocal(recordstore.BackendSQLite)
			appendedRef(ctx, results, 30)
			_, err := results.Ref(ctx, "run-1", from, to)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("past the high seq", int64(5), int64(31), "past its high seq 30"),
		Entry("starting after it ends", int64(12), int64(10), "starts after it ends"),
		Entry("a negative seq", int64(-1), int64(0), "negative"),
	)

	It("refuses a stream that does not exist with recordstore.ErrNotFound", func() {
		results, _ := openLocal(recordstore.BackendSQLite)
		_, err := results.Ref(ctx, "run-404", 0, 0)
		Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue(), "Ref: %v", err)
	})

	DescribeTable("converts to the session status EventsRef whose JSON is its own, both ways",
		func(ref func() recordresults.StreamRef) {
			want := ref()
			events := want.EventsRef()
			refJSON, err := json.Marshal(want)
			Expect(err).ToNot(HaveOccurred())
			eventsJSON, err := json.Marshal(events)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventsJSON).To(MatchJSON(refJSON))

			var decoded query.EventsRef
			Expect(json.Unmarshal(refJSON, &decoded)).To(Succeed())
			Expect(&decoded).To(Equal(events))
			var back recordresults.StreamRef
			Expect(json.Unmarshal(eventsJSON, &back)).To(Succeed())
			Expect(back).To(Equal(want))
		},
		Entry("a local sqlite stream, with host and file", func() recordresults.StreamRef {
			results, _ := openLocal(recordstore.BackendSQLite)
			appendedRef(ctx, results, 4)
			ref, err := results.Ref(ctx, "run-1", 2, 3)
			Expect(err).ToNot(HaveOccurred())
			Expect(ref.Store.File).ToNot(BeEmpty())
			return ref
		}),
		Entry("a kv stream, with neither host nor file", func() recordresults.StreamRef {
			expires := time.Now().Add(time.Hour).UTC().Truncate(time.Millisecond)
			return recordresults.StreamRef{
				Stream: "run-kv", Kind: "sample_event", Generation: "4f9c", Low: 1, High: 7, From: 1, To: 7, Total: 7,
				ExpiresAt: &expires, Store: recordresults.StoreLocation{Backend: recordstore.BackendKV},
			}
		}),
	)
})
