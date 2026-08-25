package db_test

import (
	"context"
	"fmt"
	"testing"

	rpchttp "github.com/flanksource/clicky/rpc/http"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// timingRow is the destination for the seeded table below; the plugin is
// installed by NewGorm, so these run against a handle built the way production
// builds one. The test package is external because dbtest itself imports db.
type timingRow struct {
	ID int
}

func seedTimingRows(t *testing.T, name string, rows int) *gorm.DB {
	t.Helper()
	gormDB := dbtest.ForT(t, dbtest.Options{Name: name}).Gorm()
	require.NoError(t, gormDB.Exec("CREATE TABLE timing_row (id int primary key)").Error)
	for id := 1; id <= rows; id++ {
		require.NoError(t, gormDB.Exec("INSERT INTO timing_row (id) VALUES (?)", id).Error)
	}
	return gormDB
}

func TestServerTimingPluginCountsQueriesAndRowsReturned(t *testing.T) {
	const wantRows = 7
	gormDB := seedTimingRows(t, "server_timing", wantRows)
	ctx, timings := rpchttp.WithTimings(context.Background())

	var found []timingRow
	require.NoError(t, gormDB.WithContext(ctx).Table("timing_row").Find(&found).Error)
	require.Len(t, found, wantRows)

	queries, ok := timings.Counter("sql", "queries")
	require.True(t, ok, "a statement on a timed context records a query")
	require.Equal(t, int64(1), queries)

	rows, ok := timings.Counter("sql", "rows_returned")
	require.True(t, ok)
	require.Equal(t, int64(wantRows), rows)

	elapsed, ok := timings.Duration("sql")
	require.True(t, ok)
	require.Positive(t, elapsed, "the statement's wall time is attributed to sql")

	require.Regexp(t,
		fmt.Sprintf(`^sql;dur=[0-9.]+;desc="queries=1 rows_returned=%d"$`, wantRows),
		timings.Header())
}

// Scan() and Row() stream their rows after the callback chain has returned, so
// GORM cannot report a count for them. They still contribute their duration and
// a query, and deliberately leave rows_returned alone rather than adding a zero
// that would read as "this query returned nothing".
func TestServerTimingPluginCountsRawStatementsWithoutRows(t *testing.T) {
	gormDB := seedTimingRows(t, "server_timing_raw", 3)
	ctx, timings := rpchttp.WithTimings(context.Background())

	var ids []int
	require.NoError(t, gormDB.WithContext(ctx).Raw("SELECT id FROM timing_row").Scan(&ids).Error)
	require.Len(t, ids, 3)

	queries, ok := timings.Counter("sql", "queries")
	require.True(t, ok)
	require.Equal(t, int64(1), queries)

	_, ok = timings.Counter("sql", "rows_returned")
	require.False(t, ok, "a row count GORM never produced must not be reported as zero")
}

func TestServerTimingPluginIgnoresUntimedContexts(t *testing.T) {
	gormDB := seedTimingRows(t, "server_timing_untimed", 1)
	_, timings := rpchttp.WithTimings(context.Background())

	var found []timingRow
	require.NoError(t, gormDB.WithContext(context.Background()).Table("timing_row").Find(&found).Error)
	require.Len(t, found, 1)

	_, ok := timings.Counter("sql", "queries")
	require.False(t, ok, "a statement outside the request's context must not be attributed to it")
	require.Empty(t, timings.Header())
}
