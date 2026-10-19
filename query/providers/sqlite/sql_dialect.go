package sqlite

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/pkg/allowlist"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/providers/internal/providerkit"
	"github.com/flanksource/sqlglot-go/sqlglot"
)

// sqlDialect is the engine a statement is built for.
type sqlDialect string

const (
	dialectPostgres   sqlDialect = models.ConnectionTypePostgres
	dialectMySQL      sqlDialect = models.ConnectionTypeMySQL
	dialectSQLServer  sqlDialect = models.ConnectionTypeSQLServer
	dialectClickHouse sqlDialect = models.ConnectionTypeClickHouse
	dialectSQLite     sqlDialect = models.ConnectionTypeSQLite
)

// validSQLIdentifier is the only shape a profile-supplied backend field may
// take. A field is authored in a profile, so anything outside this shape is a
// mistake in the profile and worth reading as one rather than sanitised into
// something that runs.
var validSQLIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]{0,127}$`)

// sqlIdentifierAlphabet is every byte validSQLIdentifier admits. A validated
// identifier is copied out of this constant rather than passed through, so the
// name an Identifier node carries is one this package spelled out. Quoting it
// is the generator's job, which escapes whatever a quoted name holds.
const sqlIdentifierAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_$"

// sqlConnect resolves, hydrates and opens the request's connection, and reports
// the dialect it resolved to.
//
// The dialect is a return value rather than something a caller re-derives,
// because it is only knowable here: a `provider.type: sql` profile naming a
// stored connection does not know its own engine until that connection has been
// hydrated.
func sqlConnect(ctx context.Context, req sqlConnectRequest) (*sql.DB, sqlDialect, func(), error) {
	connType := req.ConnType
	if connType == "" {
		connType = req.Options.Type
	}

	conn := connection.SQLConnection{
		ConnectionName: req.Connection,
		Type:           connType,
	}
	if req.Options.URL != "" {
		resolveType := connType
		if resolveType == "" {
			resolveType = models.ConnectionTypePostgres
		}
		resolved, err := providerkit.ResolveInlineURL(ctx, req.Options.URL, resolveType)
		if err != nil {
			return nil, "", nil, err
		}
		conn.URL.ValueStatic = resolved
	}

	if err := conn.HydrateConnection(ctx); err != nil {
		return nil, "", nil, fmt.Errorf("failed to hydrate sql connection: %w", err)
	}
	release, err := ctx.AcquireConnectionLease(req.Connection)
	if err != nil {
		return nil, "", nil, fmt.Errorf("acquire sql connection lease: %w", err)
	}
	if req.Options.Database != "" {
		hydrated, err := conn.UseDatabase(req.Options.Database)
		if err != nil {
			release()
			return nil, "", nil, fmt.Errorf("failed to select sql database: %w", err)
		}
		conn = hydrated
	}

	client, err := conn.Client(ctx)
	if err != nil {
		release()
		return nil, "", nil, fmt.Errorf("failed to create sql client: %w", err)
	}
	dialect := sqlDialect(conn.Type)
	if dialect == "" {
		dialect = dialectPostgres
	}
	return client, dialect, release, nil
}

// sqlConnectRequest is what sqlConnect needs from a provider request, named so
// the four values travel together instead of as positional arguments.
type sqlConnectRequest struct {
	Connection string
	ConnType   string
	Options    sqlOptions
}

// fieldColumn validates a profile-supplied backend field and returns the
// column it names.
//
// Validation and the node are one call because there is no correct way to
// build the second without the first, and a caller must not be able to reach
// for the second alone.
func fieldColumn(field string) (sqlNode, error) {
	name, ok := allowlist.Copy(field, sqlIdentifierAlphabet)
	if !ok || !validSQLIdentifier.MatchString(name) {
		return nil, fmt.Errorf(
			"backend field %q is not a plain column name; a filtered column must name one column of the query's result",
			field)
	}
	return columnNode(name), nil
}

func validateSQLIdentifierPath(identifier string) ([]string, error) {
	parts := strings.Split(identifier, ".")
	for index, part := range parts {
		name, ok := allowlist.Copy(part, sqlIdentifierAlphabet)
		if !ok || !validSQLIdentifier.MatchString(name) {
			return nil, fmt.Errorf(
				"%q is not a valid SQL identifier; use plain names separated by dots", identifier)
		}
		parts[index] = name
	}
	return parts, nil
}

// setPageTail takes limit rows after skipping offset, which T-SQL spells
// differently from everyone else. Both are ints the caller chose, never user
// text, so they are literals rather than bound — which also sidesteps
// ClickHouse's client-side binder and T-SQL's refusal to parameterise FETCH
// NEXT.
func setPageTail(dialect sqlDialect, selection sqlNode, limit, offset int) {
	if dialect == dialectSQLServer {
		selection.Set("limit", sqlglot.New("Fetch", kv("direction", "NEXT"), kv("count", number(limit)),
			kv("limit_options", sqlglot.New("LimitOptions",
				kv("percent", false), kv("rows", true), kv("with_ties", false)))))
		selection.Set("offset", sqlglot.New("Offset", kv("expression", number(offset))))
		return
	}
	selection.Set("limit", sqlglot.New("Limit", kv("expression", number(limit))))
	if offset > 0 {
		selection.Set("offset", sqlglot.New("Offset", kv("expression", number(offset))))
	}
}

// ordered is one ORDER BY term. nullsFirst states where nulls go; the
// generator writes NULLS FIRST/LAST only where that is not the dialect's
// default, and MySQL and T-SQL, which cannot say it, write nothing — which is
// why their nulls-last order is spelled with an explicit leading term.
func ordered(this sqlNode, desc, nullsFirst bool) sqlNode {
	return sqlglot.New("Ordered", kv("this", this), kv("desc", desc), kv("nulls_first", nullsFirst))
}

// defaultNullsFirst is where dialect puts nulls when an order does not say:
// postgres sorts them as the largest value, ClickHouse always last, and the
// rest as the smallest.
func defaultNullsFirst(dialect sqlDialect, desc bool) bool {
	switch dialect {
	case dialectPostgres:
		return desc
	case dialectClickHouse:
		return false
	default:
		return !desc
	}
}

// nullsLastKey is `CASE WHEN column IS NULL THEN 1 ELSE 0 END`, the leading
// term that puts nulls after every value where NULLS LAST cannot be written.
func nullsLastKey(column sqlNode) sqlNode {
	when := sqlglot.New("If", kv("this", isNull(column)), kv("true", number(1)))
	key := sqlglot.New("Case", kv("ifs", sqlNodes(when)), kv("default", number(0)))
	return sqlglot.New("Ordered", kv("this", key), kv("nulls_first", true))
}

// arbitraryOrder is ORDER BY (SELECT NULL): T-SQL takes OFFSET … FETCH only
// after an ORDER BY, and an unordered page claims no order.
func arbitraryOrder() sqlNode {
	none := sqlglot.New("Select", kv("expressions", sqlNodes(sqlglot.New("Null"))))
	return sqlglot.New("Ordered", kv("this", sqlglot.New("Subquery", kv("this", none))), kv("nulls_first", true))
}

// likeEscapeChar is "!" rather than "\" because MySQL's NO_BACKSLASH_ESCAPES
// mode and ClickHouse's fixed backslash handling disagree about the latter.
const likeEscapeChar = "!"

// likeMatch is a case-insensitive substring predicate against column, with the
// pattern for needle bound. ILIKE is postgres-only, so every dialect folds
// case explicitly instead.
func (s *sqlStatement) likeMatch(column sqlNode, needle string) sqlNode {
	return s.likeAnchored(column, "%", needle, "%")
}

// likeAnchored is a case-insensitive LIKE of needle between the wildcards
// before and after — "%" or nothing — with the pattern bound.
func (s *sqlStatement) likeAnchored(column sqlNode, before, needle, after string) sqlNode {
	lowered := sqlglot.New("Lower", kv("this", column.Copy()))
	if s.dialect == dialectClickHouse {
		// ClickHouse's LIKE takes no ESCAPE clause, so its metacharacters are
		// escaped with the backslash it does understand.
		return binary("Like", lowered, s.bind(before+escapeLikeNeedle(needle, `\`, s.dialect)+after))
	}
	like := binary("Like", lowered, s.bind(before+escapeLikeNeedle(needle, likeEscapeChar, s.dialect)+after))
	return binary("Escape", like, stringLiteral(likeEscapeChar))
}

// globMatch is a match pattern as a case-insensitive predicate against column:
// MatchItem's reading of the pattern, in the dialect's own comparison.
func (s *sqlStatement) globMatch(column sqlNode, pattern query.MatchPattern) sqlNode {
	switch pattern.Mode {
	case query.MatchAny:
		return binary("EQ", number(1), number(1))
	case query.MatchPrefix:
		return s.likeAnchored(column, "", pattern.Text, "%")
	case query.MatchSuffix:
		return s.likeAnchored(column, "%", pattern.Text, "")
	case query.MatchContains:
		return s.likeAnchored(column, "%", pattern.Text, "%")
	default:
		lowered := sqlglot.New("Lower", kv("this", column.Copy()))
		return binary("EQ", lowered, s.bind(strings.ToLower(pattern.Text)))
	}
}

// escapeLikeNeedle neutralises the wildcards in a typed search term, so
// searching for "50%" finds a literal "50%" rather than everything after "50".
func escapeLikeNeedle(needle, escape string, d sqlDialect) string {
	specials := []string{escape, "%", "_"}
	if d == dialectSQLServer {
		// T-SQL also reads "[" as the start of a character class.
		specials = append(specials, "[")
	}
	lowered := strings.ToLower(needle)
	var out strings.Builder
	for _, char := range lowered {
		for _, special := range specials {
			if string(char) == special {
				out.WriteString(escape)
				break
			}
		}
		out.WriteRune(char)
	}
	return out.String()
}
