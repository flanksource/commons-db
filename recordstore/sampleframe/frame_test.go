package sampleframe_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
	"github.com/flanksource/commons-db/recordstore/sampleframe"
	"github.com/flanksource/commons-db/recordstore/sqlite"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestFrames(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Complete sample frames") }

var _ = Describe("complete sample publication", func() {
	var writer *sampleframe.Writer
	var backend *sqlite.Backend
	ctx := context.Background()
	at := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	BeforeEach(func() {
		var err error
		backend, err = sqlite.Open(sqlite.Options{Path: filepath.Join(GinkgoT().TempDir(), "samples.sqlite"), Schema: recordstoretest.Schema, SweepInterval: time.Hour})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(backend.Close)
		schema, err := recordstoretest.Schema(recordstoretest.Kind)
		Expect(err).NotTo(HaveOccurred())
		writer = &sampleframe.Writer{Backend: backend, Schema: schema, Stream: "sample-run"}
	})
	It("keeps empty samples distinct and gives identical-time samples exact membership", func() {
		first, err := writer.Append(ctx, sampleframe.Frame{Source: "db", StartedAt: at, CompletedAt: at, Observed: 2}, recordstoretest.SampleRows(1, 2))
		Expect(err).NotTo(HaveOccurred())
		empty, err := writer.Append(ctx, sampleframe.Frame{Source: "db", StartedAt: at, CompletedAt: at}, nil)
		Expect(err).NotTo(HaveOccurred())
		last, err := writer.Append(ctx, sampleframe.Frame{Source: "db", StartedAt: at, CompletedAt: at, Observed: 1}, recordstoretest.SampleRows(3, 3))
		Expect(err).NotTo(HaveOccurred())
		Expect([]int64{first.AfterSeq, first.ToSeq, empty.AfterSeq, empty.ToSeq, last.AfterSeq, last.ToSeq}).To(Equal([]int64{0, 2, 2, 2, 2, 3}))
		Expect([]string{first.ID, empty.ID, last.ID}).To(ConsistOf(HaveLen(36), HaveLen(36), HaveLen(36)))
		Expect(first.ID).NotTo(Equal(empty.ID))
		meta, err := backend.Meta(ctx, writer.Stream+"-frames")
		Expect(err).NotTo(HaveOccurred())
		Expect(meta.Total).To(Equal(int64(3)))
	})
	It("does not publish completion metadata when rows fail validation", func() {
		_, err := writer.Append(ctx, sampleframe.Frame{Source: "db", StartedAt: at, CompletedAt: at, Observed: 1}, []recordstore.Row{{"missing": true}})
		Expect(err).To(HaveOccurred())
		_, err = backend.Meta(ctx, writer.Stream+"-frames")
		Expect(err).To(MatchError(recordstore.ErrNotFound))
	})
	It("rejects invalid bounds and retained counts before writing", func() {
		_, err := writer.Append(ctx, sampleframe.Frame{Source: "db", StartedAt: at.Add(time.Second), CompletedAt: at}, nil)
		Expect(err).To(HaveOccurred())
		_, err = writer.Append(ctx, sampleframe.Frame{Source: "db", StartedAt: at, CompletedAt: at}, recordstoretest.SampleRows(1, 2))
		Expect(err).To(HaveOccurred())
	})
	It("seals an empty capture without inventing a completed sample", func() {
		Expect(writer.Close(ctx)).To(Succeed())
		meta, err := backend.Meta(ctx, writer.Stream+"-frames")
		Expect(err).NotTo(HaveOccurred())
		Expect(meta.HighSeq).To(BeZero())
		Expect(meta.Sealed).To(BeTrue())
	})
})
