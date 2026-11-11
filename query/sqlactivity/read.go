package sqlactivity

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Snapshot is one read of an engine: the server's time and its requests.
type Snapshot struct {
	ServerTime time.Time
	Rows       []Sample
}

// Filter narrows a read. Idle reads sessions that run nothing too; SessionID,
// when not zero, reads only that session (SQL Server SPID, Postgres backend
// pid).
type Filter struct {
	Idle      bool
	SessionID int64
}

// Read reads engine's sessions through db, which must not be the connection
// a sampled session runs on: the reader excludes its own session. Every row
// is stamped with the engine and derived from the server's time.
func Read(ctx context.Context, db *sql.DB, engine string, filter Filter) (Snapshot, error) {
	var snapshot Snapshot
	var err error
	switch engine {
	case EnginePostgres:
		snapshot, err = readPostgres(ctx, db, filter)
	case EngineSQLServer:
		snapshot, err = readSQLServer(ctx, db, filter)
	default:
		return Snapshot{}, fmt.Errorf("sql activity: engine %q is none of %s, %s", engine, EnginePostgres, EngineSQLServer)
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("read %s activity: %w", engine, err)
	}
	if snapshot.ServerTime.IsZero() {
		return Snapshot{}, fmt.Errorf("%s activity snapshot has no server timestamp", engine)
	}
	for i := range snapshot.Rows {
		snapshot.Rows[i].Engine = engine
		snapshot.Rows[i].Derive(snapshot.ServerTime)
	}
	return snapshot, nil
}
