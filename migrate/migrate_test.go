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
