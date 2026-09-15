//go:build benchmark

package recordresults_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
)

const benchmarkMaxDatasetBytes = 1 << 30

var (
	benchmarkRecordSizes  = []int{4 << 10, 32 << 10, 128 << 10, 512 << 10}
	benchmarkRecordCounts = []int{128, 1024, 8192, 32000}
)

type benchmarkCase struct {
	recordBytes int
	records     int
}

func (c benchmarkCase) name() string {
	return fmt.Sprintf("size=%dKiB/records=%d", c.recordBytes>>10, c.records)
}

func (c benchmarkCase) datasetBytes() int64 {
	return int64(c.recordBytes) * int64(c.records)
}

func benchmarkCases() []benchmarkCase {
	cases := make([]benchmarkCase, 0, len(benchmarkRecordSizes)*len(benchmarkRecordCounts))
	for _, recordBytes := range benchmarkRecordSizes {
		for _, records := range benchmarkRecordCounts {
			benchmarkCase := benchmarkCase{recordBytes: recordBytes, records: records}
			if benchmarkCase.datasetBytes() <= benchmarkMaxDatasetBytes {
				cases = append(cases, benchmarkCase)
			}
		}
	}
	return cases
}

var benchmarkStart = time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)

type benchmarkRecordShape struct {
	At      time.Time `json:"at"`
	Group   string    `json:"group"`
	Ordinal int       `json:"ordinal"`
	Payload string    `json:"payload" filter:"-"`
	Tags    []string  `json:"tags"`
}

func benchmarkRows(benchmarkCase benchmarkCase) []recordstore.Row {
	rows := make([]recordstore.Row, benchmarkCase.records)
	paddings := map[int]string{}
	for index := range rows {
		ordinal := index + 1
		parity := "odd"
		if ordinal%2 == 0 {
			parity = "even"
		}
		row := recordstore.Row{
			"at":      benchmarkStart.Add(time.Duration(index) * time.Millisecond).Format("2006-01-02T15:04:05.000000000Z07:00"),
			"group":   fmt.Sprintf("group-%02d", ordinal%8),
			"ordinal": ordinal,
			"payload": "",
			"tags":    []string{"all", parity},
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			panic(fmt.Sprintf("encode benchmark record %d: %v", ordinal, err))
		}
		paddingBytes := benchmarkCase.recordBytes - len(encoded)
		if paddingBytes < 0 {
			panic(fmt.Sprintf("benchmark record metadata is %d bytes, over target %d", len(encoded), benchmarkCase.recordBytes))
		}
		padding, ok := paddings[paddingBytes]
		if !ok {
			padding = strings.Repeat("x", paddingBytes)
			paddings[paddingBytes] = padding
		}
		row["payload"] = padding
		rows[index] = row
	}
	return rows
}

var _ = Describe("recordstore benchmark workload", func() {
	It("includes every requested matrix point at or below one GiB", func() {
		cases := benchmarkCases()
		Expect(cases).To(HaveLen(13))

		included := make(map[string]bool, len(cases))
		for _, benchmarkCase := range cases {
			Expect(benchmarkCase.datasetBytes()).To(BeNumerically("<=", benchmarkMaxDatasetBytes))
			included[benchmarkCase.name()] = true
		}
		Expect(included).To(And(
			HaveKey("size=32KiB/records=32000"),
			HaveKey("size=128KiB/records=8192"),
			Not(HaveKey("size=128KiB/records=32000")),
			Not(HaveKey("size=512KiB/records=8192")),
			Not(HaveKey("size=512KiB/records=32000")),
		))
	})

	It("generates records with the requested JSON size and deterministic query fields", func() {
		for _, size := range benchmarkRecordSizes {
			rows := benchmarkRows(benchmarkCase{recordBytes: size, records: 8})
			Expect(rows).To(HaveLen(8))
			for index, row := range rows {
				encoded, err := json.Marshal(row)
				Expect(err).ToNot(HaveOccurred())
				Expect(encoded).To(HaveLen(size), "record %d at size %d", index, size)
				Expect(row).To(HaveKeyWithValue("ordinal", index+1))
			}
			Expect(rows[0]).To(HaveKeyWithValue("group", "group-01"))
			Expect(rows[7]).To(HaveKeyWithValue("group", "group-00"))
			Expect(rows[0]).To(HaveKeyWithValue("tags", []string{"all", "odd"}))
			Expect(rows[1]).To(HaveKeyWithValue("tags", []string{"all", "even"}))
		}
	})
})
