package db

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultPoolConfigIsBoundedAndDoesNotChurn(t *testing.T) {
	cfg := DefaultPoolConfig()

	assert.Positive(t, cfg.MaxOpenConns, "an unbounded pool lets a burst of goroutines open a backend each")
	assert.Equal(t, cfg.MaxOpenConns, cfg.MaxIdleConns,
		"idle below open makes a bursty workload reconnect for every connection above the idle ceiling")
	assert.Positive(t, cfg.ConnMaxIdleTime, "idle connections must eventually be released")
	assert.Greater(t, cfg.ConnMaxLifetime, cfg.ConnMaxIdleTime,
		"the lifetime cap is a backstop for connections that never go idle")
}

// sql.Open is lazy, so this pins NewDB's pool settings without a live server.
func TestNewDBBoundsThePool(t *testing.T) {
	db, err := NewDB("postgres://postgres:postgres@127.0.0.1:1/unreachable?sslmode=disable")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	assert.Equal(t, DefaultPoolConfig().MaxOpenConns, db.Stats().MaxOpenConnections,
		"NewDB must hand back a bounded pool rather than database/sql's unlimited default")
}

func TestPoolConfigApplyBoundsOpenConnections(t *testing.T) {
	db, err := sql.Open("pgx", "postgres://postgres:postgres@127.0.0.1:1/unreachable?sslmode=disable")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	PoolConfig{MaxOpenConns: 3, MaxIdleConns: 3}.Apply(db)
	assert.Equal(t, 3, db.Stats().MaxOpenConnections)
}
