package recordresults_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
)

var _ = Describe("recordstore benchmark storage", func() {
	for _, backend := range []benchmarkBackend{benchmarkKVMemory, benchmarkNDJSON, benchmarkSQLite, benchmarkSQLiteCompressed} {
		backend := backend
		It("measures stored size for "+string(backend), func() {
			benchmarkCase := benchmarkCase{recordBytes: 4 << 10, records: 128}
			schemas := recordstore.NewSchemas()
			registerBenchmarkSchema(GinkgoT(), schemas, backend)
			source := openBenchmarkSource(GinkgoT(), backend, schemas)
			DeferCleanup(source.Close, GinkgoT())
			appendBenchmarkRows(GinkgoT(), source.backend, benchmarkRows(benchmarkCase), benchmarkCase.records)

			storedBytes, basis, err := measureBenchmarkStorage(context.Background(), source)
			Expect(err).ToNot(HaveOccurred())
			Expect(storedBytes).To(BeNumerically(">", 0))
			Expect(basis).ToNot(BeEmpty())
			Expect(compressionRatio(benchmarkCase.datasetBytes(), storedBytes)).To(BeNumerically(">", 0))
		})
	}

	It("stores a compressed payload column in fewer bytes than a plain one", func() {
		benchmarkCase := benchmarkCase{recordBytes: 4 << 10, records: 128}
		stored := map[benchmarkBackend]int64{}
		for _, backend := range []benchmarkBackend{benchmarkSQLite, benchmarkSQLiteCompressed} {
			schemas := recordstore.NewSchemas()
			registerBenchmarkSchema(GinkgoT(), schemas, backend)
			source := openBenchmarkSource(GinkgoT(), backend, schemas)
			DeferCleanup(source.Close, GinkgoT())
			appendBenchmarkRows(GinkgoT(), source.backend, benchmarkRows(benchmarkCase), benchmarkCase.records)
			_, rows := recordstoretest.Scanned(source.backend, benchmarkStream, 0)
			Expect(rows).To(HaveLen(benchmarkCase.records))
			bytes, _, err := measureBenchmarkStorage(context.Background(), source)
			Expect(err).ToNot(HaveOccurred())
			stored[backend] = bytes
		}
		Expect(stored[benchmarkSQLiteCompressed]).To(BeNumerically("<", stored[benchmarkSQLite]))
	})
})

func BenchmarkRecordStoreStorage(b *testing.B) {
	for _, backend := range benchmarkBackends {
		for _, benchmarkCase := range benchmarkCases() {
			b.Run(string(backend)+"/"+benchmarkCase.name(), func(b *testing.B) {
				rows := benchmarkRows(benchmarkCase)
				schemas := recordstore.NewSchemas()
				registerBenchmarkSchema(b, schemas, backend)
				source := openBenchmarkSource(b, backend, schemas)
				appendBenchmarkRows(b, source.backend, rows, benchmarkCase.records)

				storedBytes, _, err := measureBenchmarkStorage(context.Background(), source)
				if err != nil {
					source.Close(b)
					b.Fatalf("measure benchmark storage: %v", err)
				}
				b.ReportMetric(float64(storedBytes)/(1<<20), "storage-MiB")
				b.ReportMetric(compressionRatio(benchmarkCase.datasetBytes(), storedBytes), "compression-ratio")
				source.Close(b)
			})
		}
	}
}
