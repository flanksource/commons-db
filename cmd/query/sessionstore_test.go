package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/query"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func startSessionStoreDB(t *testing.T) *gorm.DB {
	t.Helper()
	if os.Getenv("COMMONS_DB_EMBEDDED_TEST") == "" {
		t.Skip("set COMMONS_DB_EMBEDDED_TEST=1 to run embedded-postgres integration tests")
	}
	dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:  filepath.Join(t.TempDir(), "postgres"),
		Database: "query_sessions",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stop()) })

	gdb, pool, err := commonsdb.SetupDB(dsn, "query-sessions-test")
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, migrateSchema(t.Context(), dsn))
	return gdb
}

func runningSession(id string) query.SessionInfo {
	return query.SessionInfo{
		ID:        id,
		Profile:   "cpu-top",
		Kind:      query.KindTop,
		State:     query.SessionRunning,
		Params:    map[string]any{"namespace": "prod"},
		StartedAt: time.Now(),
	}
}

func TestSessionStorePersistsTransitionsAndEvents(t *testing.T) {
	store := NewSessionStore(startSessionStoreDB(t), 7*24*time.Hour)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	info := runningSession("sess-1")
	store.OnTransition(info)

	for i := 1; i <= 3; i++ {
		store.OnEvent(query.Event{
			SessionID: "sess-1",
			Sequence:  int64(i),
			Time:      time.Now(),
			Row:       query.Row{"n": float64(i)},
		})
	}
	require.NoError(t, store.Flush())

	events, err := store.Events("sess-1")
	require.NoError(t, err)
	require.Len(t, events, 3)
	require.Equal(t, int64(1), events[0].Sequence)
	require.Equal(t, float64(2), events[1].Row["n"])

	stopped := time.Now()
	info.State = query.SessionCompleted
	info.EventCount = 3
	info.StoppedAt = &stopped
	store.OnTransition(info)

	got, ok, err := store.Get("sess-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, query.SessionCompleted, got.State)
	require.Equal(t, int64(3), got.EventCount)
	require.NotNil(t, got.StoppedAt)
	require.Equal(t, "prod", got.Params["namespace"])

	list, err := store.List()
	require.NoError(t, err)
	require.Len(t, list, 1)
}

func TestSessionStoreFlushesFullBatchesWithoutFlush(t *testing.T) {
	store := NewSessionStore(startSessionStoreDB(t), 7*24*time.Hour)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	store.OnTransition(runningSession("sess-batch"))
	for i := 1; i <= sessionEventBatchSize; i++ {
		store.OnEvent(query.Event{SessionID: "sess-batch", Sequence: int64(i), Time: time.Now(), Row: query.Row{"n": i}})
	}

	events, err := store.Events("sess-batch")
	require.NoError(t, err)
	require.Len(t, events, sessionEventBatchSize, "a full batch flushes synchronously")
}

func TestSessionStoreMarksInterruptedOnStartup(t *testing.T) {
	db := startSessionStoreDB(t)
	store := NewSessionStore(db, 7*24*time.Hour)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	store.OnTransition(runningSession("sess-orphan"))
	done := runningSession("sess-done")
	done.State = query.SessionCompleted
	store.OnTransition(done)

	require.NoError(t, store.MarkInterrupted())

	orphan, ok, err := store.Get("sess-orphan")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, query.SessionInterrupted, orphan.State)

	finished, _, err := store.Get("sess-done")
	require.NoError(t, err)
	require.Equal(t, query.SessionCompleted, finished.State, "terminal sessions are untouched")
}

func TestSessionStorePrunesExpiredSessionsAndEvents(t *testing.T) {
	db := startSessionStoreDB(t)
	store := NewSessionStore(db, time.Hour)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	old := runningSession("sess-old")
	old.State = query.SessionCompleted
	longAgo := time.Now().Add(-2 * time.Hour)
	old.StoppedAt = &longAgo
	store.OnTransition(old)
	store.OnEvent(query.Event{SessionID: "sess-old", Sequence: 1, Time: longAgo, Row: query.Row{"n": 1}})
	require.NoError(t, store.Flush())

	fresh := runningSession("sess-fresh")
	store.OnTransition(fresh)

	require.NoError(t, store.Prune())

	_, ok, err := store.Get("sess-old")
	require.NoError(t, err)
	require.False(t, ok, "expired session is pruned")

	events, err := store.Events("sess-old")
	require.NoError(t, err)
	require.Empty(t, events, "events cascade-delete with the session")

	_, ok, err = store.Get("sess-fresh")
	require.NoError(t, err)
	require.True(t, ok)
}
