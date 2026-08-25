package connection

import (
	"testing"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestClickHouseSettings(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ClickHouse Settings Suite")
}

var _ = Describe("ClickHouse query settings", func() {
	It("applies safe defaults to every ClickHouse client", func() {
		options, err := (SQLConnection{
			Type: models.ConnectionTypeClickHouse,
			URL:  types.EnvVar{ValueStatic: "clickhouse://localhost:9000/default"},
		}).clickHouseOptions()

		Expect(err).ToNot(HaveOccurred())
		Expect(options.Settings).To(SatisfyAll(
			HaveKeyWithValue(models.ClickHousePropertyMaxExecutionTime, models.ClickHouseDefaultMaxExecutionTime),
			HaveKeyWithValue(models.ClickHousePropertyMaxRowsToRead, models.ClickHouseDefaultMaxRowsToRead),
			HaveKeyWithValue(models.ClickHousePropertyMaxBytesToRead, models.ClickHouseDefaultMaxBytesToRead),
			HaveKeyWithValue(models.ClickHousePropertyMaxMemoryUsage, models.ClickHouseDefaultMaxMemoryUsage),
			HaveKeyWithValue(models.ClickHousePropertyMaxThreads, models.ClickHouseDefaultMaxThreads),
			HaveKeyWithValue(models.ClickHousePropertyReadOverflowMode, models.ClickHouseDefaultReadOverflowMode),
			HaveKeyWithValue(models.ClickHousePropertyTimeoutOverflowMode, models.ClickHouseDefaultTimeoutOverflowMode),
		))
	})

	It("maps connection properties over DSN settings", func() {
		options, err := (SQLConnection{
			Type: models.ConnectionTypeClickHouse,
			URL:  types.EnvVar{ValueStatic: "clickhouse://localhost:9000/default?max_threads=9&log_queries=1"},
			Properties: types.JSONStringMap{
				models.ClickHousePropertyMaxExecutionTime:    "15",
				models.ClickHousePropertyMaxRowsToRead:       "2000000",
				models.ClickHousePropertyMaxBytesToRead:      "134217728",
				models.ClickHousePropertyMaxMemoryUsage:      "1073741824",
				models.ClickHousePropertyMaxThreads:          "2",
				models.ClickHousePropertyReadOverflowMode:    "break",
				models.ClickHousePropertyTimeoutOverflowMode: "break",
			},
		}).clickHouseOptions()

		Expect(err).ToNot(HaveOccurred())
		Expect(options.Settings).To(SatisfyAll(
			HaveKeyWithValue(models.ClickHousePropertyMaxExecutionTime, "15"),
			HaveKeyWithValue(models.ClickHousePropertyMaxRowsToRead, "2000000"),
			HaveKeyWithValue(models.ClickHousePropertyMaxBytesToRead, "134217728"),
			HaveKeyWithValue(models.ClickHousePropertyMaxMemoryUsage, "1073741824"),
			HaveKeyWithValue(models.ClickHousePropertyMaxThreads, "2"),
			HaveKeyWithValue(models.ClickHousePropertyReadOverflowMode, "break"),
			HaveKeyWithValue(models.ClickHousePropertyTimeoutOverflowMode, "break"),
			HaveKeyWithValue("log_queries", 1),
		))
	})

	DescribeTable("rejects invalid properties",
		func(key, value string) {
			_, err := (SQLConnection{
				Type:       models.ConnectionTypeClickHouse,
				URL:        types.EnvVar{ValueStatic: "clickhouse://localhost:9000/default"},
				Properties: types.JSONStringMap{key: value},
			}).clickHouseOptions()

			Expect(err).To(MatchError(ContainSubstring(key)))
		},
		Entry("zero max execution time", models.ClickHousePropertyMaxExecutionTime, "0"),
		Entry("negative max rows", models.ClickHousePropertyMaxRowsToRead, "-1"),
		Entry("non-numeric max bytes", models.ClickHousePropertyMaxBytesToRead, "256 MiB"),
		Entry("unknown read overflow mode", models.ClickHousePropertyReadOverflowMode, "ignore"),
		Entry("unknown timeout overflow mode", models.ClickHousePropertyTimeoutOverflowMode, "ignore"),
	)

	It("preserves connection properties without sharing their map", func() {
		model := models.Connection{
			Name:       "logs",
			Type:       models.ConnectionTypeClickHouse,
			Properties: types.JSONStringMap{models.ClickHousePropertyMaxThreads: "3"},
		}
		var sqlConnection SQLConnection

		Expect(sqlConnection.FromModel(model)).To(Succeed())
		model.Properties[models.ClickHousePropertyMaxThreads] = "8"
		Expect(sqlConnection.Properties).To(HaveKeyWithValue(models.ClickHousePropertyMaxThreads, "3"))

		roundTrip := sqlConnection.ToModel()
		roundTrip.Properties[models.ClickHousePropertyMaxThreads] = "6"
		Expect(sqlConnection.Properties).To(HaveKeyWithValue(models.ClickHousePropertyMaxThreads, "3"))
	})
})
