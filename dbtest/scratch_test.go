package dbtest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithDatabase(t *testing.T) {
	tests := []struct {
		name     string
		dsn      string
		database string
		want     string
	}{
		{
			name:     "replaces the maintenance database",
			dsn:      "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable",
			database: "scratch_1",
			want:     "postgres://postgres:postgres@localhost:5432/scratch_1?sslmode=disable",
		},
		{
			// A substring replacement of "/postgres" would no-op here and hand
			// back the caller's own database to be dropped.
			name:     "replaces a database not named postgres",
			dsn:      "postgres://user:pw@localhost:5432/production?sslmode=require",
			database: "scratch_2",
			want:     "postgres://user:pw@localhost:5432/scratch_2?sslmode=require",
		},
		{
			// A substring replacement would corrupt the password instead.
			name:     "leaves a password containing the database name intact",
			dsn:      "postgres://user:%2Fpostgres@localhost:5432/postgres",
			database: "scratch_3",
			want:     "postgres://user:%2Fpostgres@localhost:5432/scratch_3",
		},
		{
			name:     "preserves every other connection parameter",
			dsn:      "postgres://u:p@db.internal:6432/app?sslmode=verify-full&application_name=tests",
			database: "scratch_4",
			want:     "postgres://u:p@db.internal:6432/scratch_4?sslmode=verify-full&application_name=tests",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := withDatabase(tt.dsn, tt.database)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{name: "already legal", in: "migrate_runner", want: "migrate_runner"},
		{name: "lowercases", in: "MigrateRunner", want: "migraterunner"},
		{name: "collapses punctuation", in: "TestApply/sub-case #3", want: "testapply_sub_case_3"},
		{name: "trims leading and trailing separators", in: "--name--", want: "name"},
		{name: "falls back when nothing survives", in: "///", want: "dbtest"},
		{name: "truncates to the identifier limit", in: strings.Repeat("a", 100), want: strings.Repeat("a", maxIdentifier)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sanitize(tt.in))
		})
	}
}

func TestScratchNameKeepsSuffixWhenTruncating(t *testing.T) {
	const unique = "12345_7"
	name := scratchName(strings.Repeat("a", 100), unique)

	assert.LessOrEqual(t, len(name), maxIdentifier)
	// Truncating the tail instead of the base is what makes two concurrent
	// runs resolve to the same database.
	assert.True(t, strings.HasSuffix(name, "_"+unique), "got %q", name)
}

func TestScratchNameIsUniquePerBase(t *testing.T) {
	first := scratchName("runner", uniqueSuffix())
	second := scratchName("runner", uniqueSuffix())
	assert.NotEqual(t, first, second)
}

func TestRedactMasksPassword(t *testing.T) {
	got := redact("postgres://user:hunter2@localhost:5432/db?sslmode=disable")
	assert.NotContains(t, got, "hunter2")
	assert.Contains(t, got, "user")
	assert.Contains(t, got, "localhost:5432")
}

func TestRedactLeavesPasswordlessDSNAlone(t *testing.T) {
	const dsn = "postgres://user@localhost:5432/db"
	assert.Equal(t, dsn, redact(dsn))
}
