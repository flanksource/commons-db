package lease

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// sqlDescriptionChars is how much of an app-lock resource name
// sys.dm_tran_locks.resource_description shows.
const sqlDescriptionChars = 32

type sqlSession struct {
	holder []byte
	login  time.Time
	modes  []string
}

// sqlHolders reads the sessions holding a key's app locks and the holder
// names they set in CONTEXT_INFO. Since is the session's login time, which is
// at or before acquisition because every lease pins and then ends its own
// session.
func sqlHolders(ctx context.Context, db *sql.DB, req Request) ([]Holder, error) {
	name, err := sqlResource(req)
	if err != nil {
		return nil, err
	}
	if err := sqlCheckHolderPermission(ctx, db); err != nil {
		return nil, err
	}
	sessions, err := sqlHolderSessions(ctx, db, name)
	if err != nil {
		return nil, err
	}
	holders := make([]Holder, 0, len(sessions))
	for id, session := range sessions {
		mode := sqlHolderMode(session.modes)
		if mode == "" {
			continue
		}
		holder := strings.TrimRight(string(session.holder), "\x00")
		if holder == "" {
			return nil, fmt.Errorf("SQL Server session %d holds app lock %s without a holder name in CONTEXT_INFO", id, name)
		}
		holders = append(holders, Holder{Name: holder, Mode: mode, Since: session.login})
	}
	return holders, nil
}

func sqlCheckHolderPermission(ctx context.Context, db *sql.DB) error {
	var granted sql.NullInt64
	var login sql.NullString
	if err := db.QueryRowContext(ctx, sqlHolderPermission).Scan(&granted, &login); err != nil {
		return fmt.Errorf("check SQL Server VIEW SERVER STATE permission: %w", err)
	}
	if granted.Int64 != 1 {
		return fmt.Errorf("SQL Server lease holders need the VIEW SERVER STATE permission to read sys.dm_tran_locks: GRANT VIEW SERVER STATE TO [%s]", login.String)
	}
	return nil
}

func sqlHolderSessions(ctx context.Context, db *sql.DB, name string) (map[int64]*sqlSession, error) {
	expected := sqlDescribedNames(name)
	rows, err := db.QueryContext(ctx, sqlHolderLocks, "["+truncate(name, sqlDescriptionChars))
	if err != nil {
		return nil, fmt.Errorf("read SQL Server app-lock holders: %w", err)
	}
	defer func() { _ = rows.Close() }()
	sessions := map[int64]*sqlSession{}
	for rows.Next() {
		var id int64
		var mode, description string
		session := &sqlSession{}
		if err := rows.Scan(&id, &mode, &description, &session.holder, &session.login); err != nil {
			return nil, fmt.Errorf("read SQL Server app-lock holders: %w", err)
		}
		described, err := sqlDescribedName(description)
		if err != nil {
			return nil, err
		}
		if !expected[described] {
			continue
		}
		if sessions[id] != nil {
			session = sessions[id]
		}
		session.modes = append(session.modes, mode)
		sessions[id] = session
	}
	return sessions, rows.Err()
}

// sqlDescribedNames are the truncated descriptions of a key's main and
// group-bit app locks. The gate is left out: a session holding only the gate
// is waiting, not holding.
func sqlDescribedNames(name string) map[string]bool {
	names := map[string]bool{truncate(name, sqlDescriptionChars): true}
	for i := 1; i <= 256; i++ {
		names[truncate(name+":group:"+strconv.Itoa(i), sqlDescriptionChars)] = true
	}
	return names
}

// sqlDescribedName extracts the resource name from an APPLICATION
// resource_description such as "0:[deploy:oma]:(a1b2c3d4)".
func sqlDescribedName(description string) (string, error) {
	start, end := strings.Index(description, "["), strings.LastIndex(description, "]")
	if start < 0 || end < start {
		return "", fmt.Errorf("unexpected SQL Server app-lock description %q", description)
	}
	return description[start+1 : end], nil
}

// sqlHolderMode classifies a session's granted locks on a key: a writer holds
// it Exclusive, a group holder also holds the 256 group bits, and a reader
// holds it Shared. Truncation can fold the gate into the key's description,
// so a lone IntentExclusive row is a waiting writer.
func sqlHolderMode(modes []string) string {
	switch {
	case slices.Contains(modes, "X"):
		return WriteMode.String()
	case len(modes) > 2:
		return GroupMode.String()
	case slices.Contains(modes, "S"):
		return ReadMode.String()
	default:
		return ""
	}
}

func truncate(value string, n int) string {
	if len(value) <= n {
		return value
	}
	return value[:n]
}
