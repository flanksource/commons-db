// Specs for publishing a batch into a spool directory and loading it back:
// the commit point, the manifest's validation, codecs and value parity.
package spool_test

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/spool"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

// everyType has a column of every column type.
var everyType = recordstore.KindSchema{Kind: "every", Columns: []query.ColumnDef{
	{Name: "name", Type: query.ColumnTypeString},
	{Name: "number", Type: query.ColumnTypeNumber},
	{Name: "ok", Type: query.ColumnTypeBoolean},
	{Name: "at", Type: query.ColumnTypeDateTime},
	{Name: "took", Type: query.ColumnTypeDuration},
	{Name: "size", Type: query.ColumnTypeBytes},
	{Name: "status", Type: query.ColumnTypeStatus},
	{Name: "health", Type: query.ColumnTypeHealth},
	{Name: "id", Type: query.ColumnTypeUUID},
	{Name: "labels", Type: query.ColumnTypeKeyValue},
	{Name: "tags", Type: query.ColumnTypeKeyValues},
	{Name: "detail", Type: query.ColumnTypeJSON},
}, Options: recordstore.KindOptions{Key: "name", Retention: recordstore.RetainRows, OnConflict: recordstore.OnConflictReplace}}

type detail struct {
	Zeta  int    `json:"zeta"`
	Alpha string `json:"alpha"`
}

func everyRow(n int) recordstore.Row {
	at := time.Date(2026, 9, 29, 10, 0, n, 123456789, time.FixedZone("x", 3600))
	return recordstore.Row{
		"name": "row-" + string(rune('a'+n)), "number": int64(math.MaxInt64 - n), "ok": n%2 == 0,
		"at": at, "took": time.Duration(n) * time.Second, "size": int64(n * 1024), "status": "healthy",
		"health": "ok", "id": "4b6e1a2c-0000-4000-8000-00000000000" + string(rune('0'+n)),
		"labels": map[string]string{"z": "1", "a": "2"}, "tags": map[string][]string{"k": {"v1", "v2"}},
		"detail": detail{Zeta: n, Alpha: "first"},
	}
}

var _ = Describe("spool", func() {
	var (
		ctx context.Context
		dir spool.Dir
	)

	producer := recordstore.Producer{Instance: "cli-1", Seq: 7, PID: 42, Host: "host", Build: "test"}
	schemas := func(kind string) (recordstore.KindSchema, error) {
		if kind == everyType.Kind {
			return everyType, nil
		}
		return recordstore.KindSchema{}, errors.New("unknown kind")
	}

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		dir, err = spool.OpenDir(filepath.Join(GinkgoT().TempDir(), "records.sqlite.spool"))
		Expect(err).ToNot(HaveOccurred())
	})

	batchOf := func(id string, entries ...recordstore.BatchEntry) recordstore.Batch {
		return recordstore.Batch{ID: id, Producer: producer, Schemas: []recordstore.KindSchema{everyType}, Entries: entries}
	}
	appendOf := func(stream string, rows ...recordstore.Row) recordstore.BatchEntry {
		return recordstore.BatchEntry{Op: recordstore.BatchAppend, Stream: stream, Kind: everyType.Kind, Rows: rows}
	}
	loadOnly := func() (string, recordstore.Batch) {
		names, err := dir.Incoming()
		Expect(err).ToNot(HaveOccurred())
		Expect(names).To(HaveLen(1))
		batch, err := dir.Load(names[0])
		Expect(err).ToNot(HaveOccurred())
		return names[0], batch
	}
	manifestOf := func(name string) map[string]any {
		encoded, err := os.ReadFile(filepath.Join(dir.Path(), "incoming", name, "manifest.json"))
		Expect(err).ToNot(HaveOccurred())
		var manifest map[string]any
		Expect(json.Unmarshal(encoded, &manifest)).To(Succeed())
		return manifest
	}
	rewriteManifest := func(name string, change func(map[string]any)) {
		manifest := manifestOf(name)
		change(manifest)
		encoded, err := json.Marshal(manifest)
		Expect(err).ToNot(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(dir.Path(), "incoming", name, "manifest.json"), encoded, 0o600)).To(Succeed())
	}

	It("creates its directories private to the user", func() {
		for _, sub := range []string{"tmp", "incoming", "failed", "trash"} {
			info, err := os.Stat(filepath.Join(dir.Path(), sub))
			Expect(err).ToNot(HaveOccurred())
			Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o700)), sub)
		}
	})

	DescribeTable("round-trips every entry and column type through a codec",
		func(format string) {
			before := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
			batch := batchOf("b-1",
				appendOf("run-1", everyRow(1), everyRow(2)),
				appendOf("run-2"),
				recordstore.BatchEntry{Op: recordstore.BatchAppend, Stream: "run-3", Kind: everyType.Kind, Rows: []recordstore.Row{everyRow(3)}, Seal: true, Generation: "g-3"},
				recordstore.BatchEntry{Op: recordstore.BatchSeal, Stream: "run-1"},
				recordstore.BatchEntry{Op: recordstore.BatchExpire, Stream: "run-1", TTL: 90 * time.Minute},
				recordstore.BatchEntry{Op: recordstore.BatchTrim, Stream: "run-1", Before: before},
				recordstore.BatchEntry{Op: recordstore.BatchDelete, Stream: "run-2"},
				recordstore.BatchEntry{Op: recordstore.BatchReopen, Stream: "run-1", Generation: "g-1"},
			)
			name, err := dir.Publish(batch, format)
			Expect(err).ToNot(HaveOccurred())

			loadedName, loaded := loadOnly()
			Expect(loadedName).To(Equal(name))
			Expect(loaded.ID).To(Equal("b-1"))
			Expect(loaded.Producer).To(Equal(producer))
			Expect(loaded.Schemas).To(Equal([]recordstore.KindSchema{everyType}))
			Expect(loaded.Entries).To(HaveLen(8))
			Expect(loaded.Entries[0].Rows).To(HaveLen(2))
			Expect(loaded.Entries[0].Rows[0]["number"]).To(Equal(json.Number("9223372036854775806")))
			Expect(loaded.Entries[0].Rows[0]["detail"]).To(Equal(json.RawMessage(`{"zeta":1,"alpha":"first"}`)))
			Expect(loaded.Entries[0].Rows[0]["labels"]).To(Equal(json.RawMessage(`{"a":"2","z":"1"}`)))
			Expect(loaded.Entries[1].Rows).To(BeEmpty())
			Expect(loaded.Entries[2]).To(HaveField("Seal", true))
			Expect(loaded.Entries[2]).To(HaveField("Generation", "g-3"))
			Expect(loaded.Entries[3:]).To(Equal([]recordstore.BatchEntry{
				{Op: recordstore.BatchSeal, Stream: "run-1"},
				{Op: recordstore.BatchExpire, Stream: "run-1", TTL: 90 * time.Minute},
				{Op: recordstore.BatchTrim, Stream: "run-1", Before: before},
				{Op: recordstore.BatchDelete, Stream: "run-2"},
				{Op: recordstore.BatchReopen, Stream: "run-1", Generation: "g-1"},
			}))
		},
		Entry("ndjson", spool.FormatNDJSON),
		Entry("ndjson.gz", spool.FormatNDJSONGzip),
	)

	It("stores spooled rows exactly as a direct append stores them", func() {
		open := func(name string) *sqlite.Backend {
			backend, err := sqlite.Open(sqlite.Options{Path: filepath.Join(GinkgoT().TempDir(), name), Schema: schemas, TTL: time.Hour, SweepInterval: time.Hour})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(backend.Close)
			return backend
		}
		rows := []recordstore.Row{everyRow(1), everyRow(2), {"name": "sparse", "detail": "just a string", "labels": nil}}
		direct := open("direct.sqlite")
		_, err := direct.Append(ctx, "run-1", everyType.Kind, rows)
		Expect(err).ToNot(HaveOccurred())

		spooled := open("spooled.sqlite")
		_, err = dir.Publish(batchOf("b-1", appendOf("run-1", rows...)), spool.FormatNDJSONGzip)
		Expect(err).ToNot(HaveOccurred())
		_, loaded := loadOnly()
		result, err := spooled.AppendBatch(ctx, loaded)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Entries[0].Error).To(BeNil())

		Expect(rawRows(spooled)).To(Equal(rawRows(direct)))
	})

	It("spools a dynamic kind's undeclared keys and stores them as a direct append stores them", func() {
		dynamic := recordstore.KindSchema{Kind: "open", Columns: []query.ColumnDef{{Name: "name", Type: query.ColumnTypeString}},
			Options: recordstore.KindOptions{Dynamic: true, MaxDynamicColumns: 9}}
		resolver := func(string) (recordstore.KindSchema, error) { return dynamic, nil }
		open := func(name string) *sqlite.Backend {
			backend, err := sqlite.Open(sqlite.Options{Path: filepath.Join(GinkgoT().TempDir(), name), Schema: resolver, TTL: time.Hour, SweepInterval: time.Hour})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(backend.Close)
			return backend
		}
		at := time.Date(2026, 9, 30, 8, 0, 0, 5, time.UTC)
		rows := []recordstore.Row{{"name": "a", "count": int64(3), "ok": true, "at": at, "labels": map[string]any{"z": "1", "a": "2"}, "gone": nil}}
		direct := open("direct.sqlite")
		_, err := direct.Append(ctx, "run-1", dynamic.Kind, rows)
		Expect(err).ToNot(HaveOccurred())

		_, err = dir.Publish(recordstore.Batch{ID: "b-1", Producer: producer, Schemas: []recordstore.KindSchema{dynamic},
			Entries: []recordstore.BatchEntry{{Op: recordstore.BatchAppend, Stream: "run-1", Kind: dynamic.Kind, Rows: rows}}}, spool.FormatNDJSON)
		Expect(err).ToNot(HaveOccurred())
		_, loaded := loadOnly()
		Expect(loaded.Schemas).To(Equal([]recordstore.KindSchema{dynamic}))
		spooled := open("spooled.sqlite")
		result, err := spooled.AppendBatch(ctx, loaded)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Entries[0].Error).To(BeNil())

		Expect(rawTable(spooled, "records_open")).To(Equal(rawTable(direct, "records_open")))
	})

	It("commits by rename, so a batch still being written is never listed", func() {
		Expect(os.MkdirAll(filepath.Join(dir.Path(), "tmp", "cli-1-b-0"), 0o700)).To(Succeed())
		names, err := dir.Incoming()
		Expect(err).ToNot(HaveOccurred())
		Expect(names).To(BeEmpty())

		_, err = dir.Publish(batchOf("b-1", appendOf("run-1", everyRow(1))), spool.FormatNDJSON)
		Expect(err).ToNot(HaveOccurred())
		tmp, err := os.ReadDir(filepath.Join(dir.Path(), "tmp"))
		Expect(err).ToNot(HaveOccurred())
		Expect(tmp).To(HaveLen(1), "only the entry this spec made by hand")
	})

	It("lists published batches in the order they were published", func() {
		for _, id := range []string{"b-1", "b-2", "b-3"} {
			_, err := dir.Publish(batchOf(id, appendOf("run-1", everyRow(1))), spool.FormatNDJSON)
			Expect(err).ToNot(HaveOccurred())
		}
		names, err := dir.Incoming()
		Expect(err).ToNot(HaveOccurred())
		var ids []string
		for _, name := range names {
			batch, err := dir.Load(name)
			Expect(err).ToNot(HaveOccurred())
			ids = append(ids, batch.ID)
		}
		Expect(ids).To(Equal([]string{"b-1", "b-2", "b-3"}))
	})

	It("refuses to publish a batch whose rows do not fit their kind's schema", func() {
		_, err := dir.Publish(batchOf("b-1", appendOf("run-1", recordstore.Row{"name": "x", "unknown": 1})), spool.FormatNDJSON)
		Expect(err).To(MatchError(ContainSubstring(`"unknown"`)))
		_, err = dir.Publish(recordstore.Batch{ID: "b-2", Producer: producer, Entries: []recordstore.BatchEntry{appendOf("run-1", everyRow(1))}}, spool.FormatNDJSON)
		Expect(err).To(MatchError(ContainSubstring("schema")))
		_, err = dir.Publish(batchOf("b-3", appendOf("run-1", everyRow(1))), "csv")
		Expect(err).To(MatchError(ContainSubstring("csv")))
		names, err := dir.Incoming()
		Expect(err).ToNot(HaveOccurred())
		Expect(names).To(BeEmpty())
	})

	Describe("loading a batch it cannot trust", func() {
		var name string

		BeforeEach(func() {
			var err error
			name, err = dir.Publish(batchOf("b-1", appendOf("run-1", everyRow(1), everyRow(2))), spool.FormatNDJSON)
			Expect(err).ToNot(HaveOccurred())
		})

		It("refuses a manifest format it does not read with ErrManifestFormat", func() {
			rewriteManifest(name, func(manifest map[string]any) { manifest["format"] = 2 })
			_, err := dir.Load(name)
			Expect(errors.Is(err, spool.ErrManifestFormat)).To(BeTrue(), "Load: %v", err)
		})

		DescribeTable("refuses",
			func(change func(manifest map[string]any, entry map[string]any), message string) {
				rewriteManifest(name, func(manifest map[string]any) {
					change(manifest, manifest["entries"].([]any)[0].(map[string]any))
				})
				_, err := dir.Load(name)
				Expect(err).To(MatchError(ContainSubstring(message)))
			},
			Entry("a data file whose digest differs", func(_, entry map[string]any) { entry["sha256"] = "00" }, "sha256"),
			Entry("a row count other than the file holds", func(_, entry map[string]any) { entry["rows"] = 3 }, "rows"),
			Entry("a data file outside the batch", func(_, entry map[string]any) { entry["file"] = "../other.ndjson" }, "file"),
			Entry("an unknown codec", func(_, entry map[string]any) { entry["format"] = "csv" }, "csv"),
			Entry("a manifest without an id", func(manifest, _ map[string]any) { delete(manifest, "id") }, "id"),
			Entry("an unknown retention", func(manifest, _ map[string]any) {
				manifest["schemas"].([]any)[0].(map[string]any)["retention"] = "forever"
			}, "forever"),
			Entry("an invalid ttl", func(manifest, _ map[string]any) {
				manifest["entries"] = append(manifest["entries"].([]any), map[string]any{"op": "expire", "stream": "run-1", "ttl": "soon"})
			}, "soon"),
		)

		It("refuses a gzip data file that is truncated", func() {
			name, err := dir.Publish(batchOf("b-2", appendOf("run-1", everyRow(1))), spool.FormatNDJSONGzip)
			Expect(err).ToNot(HaveOccurred())
			file := filepath.Join(dir.Path(), "incoming", name, manifestOf(name)["entries"].([]any)[0].(map[string]any)["file"].(string))
			encoded, err := os.ReadFile(file)
			Expect(err).ToNot(HaveOccurred())
			Expect(os.WriteFile(file, encoded[:len(encoded)/2], 0o600)).To(Succeed())
			rewriteManifest(name, func(manifest map[string]any) {
				manifest["entries"].([]any)[0].(map[string]any)["sha256"] = spool.Digest(encoded[:len(encoded)/2])
			})
			_, err = dir.Load(name)
			Expect(err).To(Or(MatchError(ContainSubstring("EOF")), MatchError(gzip.ErrChecksum)))
		})
	})

	Describe("moving a batch on", func() {
		var name string

		BeforeEach(func() {
			var err error
			name, err = dir.Publish(batchOf("b-1", appendOf("run-1", everyRow(1))), spool.FormatNDJSON)
			Expect(err).ToNot(HaveOccurred())
		})

		It("moves an ingested batch to trash", func() {
			Expect(dir.Trash(name)).To(Succeed())
			names, err := dir.Incoming()
			Expect(err).ToNot(HaveOccurred())
			Expect(names).To(BeEmpty())
			Expect(filepath.Join(dir.Path(), "trash", name)).To(BeADirectory())
		})

		It("moves a batch it cannot ingest to failed, with the reason beside it", func() {
			Expect(dir.Fail(name, errors.New("unreadable manifest"))).To(Succeed())
			reason, err := os.ReadFile(filepath.Join(dir.Path(), "failed", name, "error.json"))
			Expect(err).ToNot(HaveOccurred())
			Expect(string(reason)).To(ContainSubstring("unreadable manifest"))
		})

		It("collects trash at once, failed batches after their age and abandoned tmp entries after theirs", func() {
			other, err := dir.Publish(batchOf("b-2", appendOf("run-1", everyRow(1))), spool.FormatNDJSON)
			Expect(err).ToNot(HaveOccurred())
			Expect(dir.Trash(name)).To(Succeed())
			Expect(dir.Fail(other, errors.New("bad"))).To(Succeed())
			abandoned := filepath.Join(dir.Path(), "tmp", "cli-9-b-9")
			Expect(os.MkdirAll(abandoned, 0o700)).To(Succeed())
			now := time.Now()

			Expect(dir.Collect(now, spool.CollectOptions{TmpAge: time.Hour, FailedAge: time.Hour})).To(Succeed())
			Expect(filepath.Join(dir.Path(), "trash", name)).ToNot(BeAnExistingFile())
			Expect(filepath.Join(dir.Path(), "failed", other)).To(BeADirectory())
			Expect(abandoned).To(BeADirectory())

			Expect(dir.Collect(now.Add(2*time.Hour), spool.CollectOptions{TmpAge: time.Hour, FailedAge: time.Hour})).To(Succeed())
			Expect(filepath.Join(dir.Path(), "failed", other)).ToNot(BeAnExistingFile())
			Expect(abandoned).ToNot(BeAnExistingFile())
		})
	})
})
