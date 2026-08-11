package schema_test

import (
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query/schema"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ClickHouse connection schema", func() {
	It("renders safe query limits as customizable connection properties", func() {
		connection := schema.ConnectionComponents()[models.ConnectionTypeClickHouse]
		properties := connection["properties"].(schema.Schema)["properties"].(schema.Schema)
		fields := properties["properties"].(schema.Schema)

		Expect(properties).To(SatisfyAll(
			HaveKeyWithValue("title", "Query limits"),
			HaveKeyWithValue("x-columns", 2),
		))
		expectedDefaults := map[string]string{
			models.ClickHousePropertyMaxExecutionTime:    models.ClickHouseDefaultMaxExecutionTime,
			models.ClickHousePropertyMaxRowsToRead:       models.ClickHouseDefaultMaxRowsToRead,
			models.ClickHousePropertyMaxBytesToRead:      models.ClickHouseDefaultMaxBytesToRead,
			models.ClickHousePropertyMaxMemoryUsage:      models.ClickHouseDefaultMaxMemoryUsage,
			models.ClickHousePropertyMaxThreads:          models.ClickHouseDefaultMaxThreads,
			models.ClickHousePropertyReadOverflowMode:    models.ClickHouseDefaultReadOverflowMode,
			models.ClickHousePropertyTimeoutOverflowMode: models.ClickHouseDefaultTimeoutOverflowMode,
		}
		Expect(fields).To(HaveLen(len(expectedDefaults)))
		for key, defaultValue := range expectedDefaults {
			Expect(fields[key].(schema.Schema)).To(HaveKeyWithValue("default", defaultValue), key)
		}
		Expect(fields[models.ClickHousePropertyMaxRowsToRead].(schema.Schema)).To(
			HaveKeyWithValue("x-clicky-unit", "count"),
		)
		for _, key := range []string{
			models.ClickHousePropertyMaxBytesToRead,
			models.ClickHousePropertyMaxMemoryUsage,
		} {
			Expect(fields[key].(schema.Schema)).To(SatisfyAll(
				HaveKeyWithValue("x-clicky-unit", "bytes"),
				Not(HaveKey("x-input-suffix")),
			), key)
		}

		for _, key := range []string{
			models.ClickHousePropertyReadOverflowMode,
			models.ClickHousePropertyTimeoutOverflowMode,
		} {
			Expect(fields[key].(schema.Schema)).To(SatisfyAll(
				HaveKeyWithValue("enum", []string{"throw", "break"}),
				HaveKeyWithValue("x-enum-display", "segmented"),
			))
		}
	})
})
