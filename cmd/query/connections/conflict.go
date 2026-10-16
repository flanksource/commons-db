package connections

import (
	"errors"
	"net/http"
	"strings"

	"github.com/flanksource/clicky/entity"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// connectionInUseCode is the StatusError code a write refused by a foreign key
// carries, so a client can branch on it instead of matching the message.
const connectionInUseCode = "connection_in_use"

// sqliteForeignKeyMessage is the fixed text SQLite raises for a RESTRICT action,
// which it compiles into a generated trigger: that failure carries
// SQLITE_CONSTRAINT_TRIGGER, not SQLITE_CONSTRAINT_FOREIGNKEY, so the message
// is the only thing that tells it apart from a user trigger's RAISE.
const sqliteForeignKeyMessage = "FOREIGN KEY constraint failed"

// classifyReferenceConflict turns a foreign-key violation into a 409, so a
// host's FK onto connections reaches the client as a refusal rather than an
// unclassified 500. PostgreSQL names the constraint and the table holding it;
// SQLite names neither, so its refusal names none. Every other error is
// returned unchanged.
func classifyReferenceConflict(err error, action, connection string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.ForeignKeyViolation {
		conflict := entity.NewStatusErrorf(http.StatusConflict, connectionInUseCode,
			"cannot %s connection %q: foreign key constraint %s on table %s refuses it",
			action, connection, pgErr.ConstraintName, pgErr.TableName)
		conflict.Context = map[string]any{"constraint": pgErr.ConstraintName, "table": pgErr.TableName}
		return conflict
	}
	if isSQLiteForeignKeyViolation(err) {
		return entity.NewStatusErrorf(http.StatusConflict, connectionInUseCode,
			"cannot %s connection %q: a foreign key constraint refuses it", action, connection)
	}
	return err
}

func isSQLiteForeignKeyViolation(err error) bool {
	var liteErr *sqlite.Error
	if !errors.As(err, &liteErr) {
		return false
	}
	switch liteErr.Code() {
	case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
		return true
	case sqlite3.SQLITE_CONSTRAINT_TRIGGER:
		return strings.Contains(liteErr.Error(), sqliteForeignKeyMessage)
	default:
		return false
	}
}
