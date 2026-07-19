package migrate

import (
	"testing"
	"testing/fstest"

	"ariga.io/atlas/sql/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithoutTableDrops(t *testing.T) {
	keep := &schema.AddTable{T: schema.NewTable("profiles")}
	drop := &schema.DropTable{T: schema.NewTable("other")}

	got := withoutTableDrops([]schema.Change{keep, drop})
	require.Len(t, got, 1)
	assert.Same(t, keep, got[0])
}

func TestApplyValidatesInputsBeforeConnecting(t *testing.T) {
	assert.EqualError(t, Apply(t.Context(), "", fstest.MapFS{}), "connection string is empty")
	assert.EqualError(t, Apply(t.Context(), "postgres://unused", nil), "schema filesystem is nil")
}

func TestConnectionWithLockTimeout(t *testing.T) {
	tests := []struct {
		name       string
		connection string
		want       string
	}{
		{
			name:       "url with sslmode but no options gains a bounded lock_timeout",
			connection: "postgres://user@localhost/app?sslmode=disable",
			want:       "postgres://user@localhost/app?options=-c+lock_timeout%3D" + migrationLockTimeout + "&sslmode=disable",
		},
		{
			name:       "existing options are preserved",
			connection: "postgres://user@localhost/app?options=-c+statement_timeout%3D5s",
			want:       "postgres://user@localhost/app?options=-c+statement_timeout%3D5s",
		},
		{
			name:       "keyword dsn is left untouched",
			connection: "host=localhost user=app dbname=app sslmode=disable",
			want:       "host=localhost user=app dbname=app sslmode=disable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, connectionWithLockTimeout(tt.connection))
		})
	}
}
