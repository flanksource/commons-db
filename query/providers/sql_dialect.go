package providers

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Masterminds/squirrel"
	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/pkg/allowlist"
)

// sqlDialect is the engine a statement is built for.
type sqlDialect string

const (
	dialectPostgres   sqlDialect = models.ConnectionTypePostgres
	dialectMySQL      sqlDialect = models.ConnectionTypeMySQL
	dialectSQLServer  sqlDialect = models.ConnectionTypeSQLServer
	dialectClickHouse sqlDialect = models.ConnectionTypeClickHouse
)

// validSQLIdentifier is the only shape a profile-supplied backend field may
// take. A field is authored in a profile, so anything outside this shape is a
// mistake in the profile and worth reading as one rather than sanitised into
// something that runs.
var validSQLIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]{0,127}$`)

// sqlIdentifierAlphabet is every byte validSQLIdentifier admits. quote copies a
// validated identifier out of this constant rather than passing the caller's
// string through, so the bytes that end up inside a SQL fragment are ones this
// package spelled out. It is the same allowlist the regexp states, applied a
// second time per byte on the way out, which is what makes the quoting safe
// without escaping: no quote character can survive the copy to be escaped.
const sqlIdentifierAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_$"

// sqlConnect resolves, hydrates and opens the request's connection, and reports
// the dialect it resolved to.
//
// The dialect is a return value rather than something a caller re-derives,
// because it is only knowable here: a `provider.type: sql` profile naming a
// stored connection does not know its own engine until that connection has been
// hydrated.
func sqlConnect(ctx context.Context, req sqlConnectRequest) (*sql.DB, sqlDialect, error) {
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
		resolved, err := resolveInlineURL(ctx, req.Options.URL, resolveType)
		if err != nil {
			return nil, "", err
		}
		conn.URL.ValueStatic = resolved
	}

	if err := conn.HydrateConnection(ctx); err != nil {
		return nil, "", fmt.Errorf("failed to hydrate sql connection: %w", err)
	}
	if req.Options.Database != "" {
		hydrated, err := conn.UseDatabase(req.Options.Database)
		if err != nil {
			return nil, "", fmt.Errorf("failed to select sql database: %w", err)
		}
		conn = hydrated
	}

	client, err := conn.Client(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create sql client: %w", err)
	}
	dialect := sqlDialect(conn.Type)
	if dialect == "" {
		dialect = dialectPostgres
	}
	return client, dialect, nil
}

// sqlConnectRequest is what sqlConnect needs from a provider request, named so
// the four values travel together instead of as positional arguments.
type sqlConnectRequest struct {
	Connection string
	ConnType   string
	Options    sqlOptions
}

// quote validates identifier and returns it quoted for d.
//
// Validation and quoting are one call because there is no correct way to do the
// second without the first, and a caller must not be able to reach for the
// second alone.
func (d sqlDialect) quote(identifier string) (string, error) {
	name, ok := allowlist.Copy(identifier, sqlIdentifierAlphabet)
	if !ok || !validSQLIdentifier.MatchString(name) {
		return "", fmt.Errorf(
			"backend field %q is not a plain column name; a filtered column must name one column of the query's result",
			identifier)
	}
	switch d {
	case dialectMySQL:
		return "`" + name + "`", nil
	case dialectSQLServer:
		return "[" + name + "]", nil
	default:
		return `"` + name + `"`, nil
	}
}

// placeholders is the bind form d's driver accepts. SQL Server takes only
// @p1..@pN, and postgres only $1..$N; the rest read a bare "?".
func (d sqlDialect) placeholders() squirrel.PlaceholderFormat {
	switch d {
	case dialectPostgres:
		return squirrel.Dollar
	case dialectSQLServer:
		return squirrel.AtP
	default:
		return squirrel.Question
	}
}

// limitTail renders "take the first n rows of this order", which T-SQL spells
// differently from everyone else. limit is an int the caller chose, never user
// text, so it is written into the statement rather than bound — which also
// sidesteps ClickHouse's client-side rewriter and T-SQL's refusal to
// parameterise FETCH NEXT.
func (d sqlDialect) limitTail(limit int) string {
	if d == dialectSQLServer {
		return fmt.Sprintf("OFFSET 0 ROWS FETCH NEXT %d ROWS ONLY", limit)
	}
	return fmt.Sprintf("LIMIT %d", limit)
}

func (d sqlDialect) pageTail(limit, offset int) string {
	if d == dialectSQLServer {
		return fmt.Sprintf("OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", offset, limit)
	}
	if offset > 0 {
		return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)
	}
	return fmt.Sprintf("LIMIT %d", limit)
}

var (
	postgresPlaceholder  = regexp.MustCompile(`\$\d+`)
	sqlServerPlaceholder = regexp.MustCompile(`@p\d+`)
)

func offsetPlaceholders(dialect sqlDialect, clause string, offset int) string {
	if offset == 0 || clause == "" {
		return clause
	}
	var marker *regexp.Regexp
	prefix := ""
	switch dialect {
	case dialectPostgres:
		marker = postgresPlaceholder
		prefix = "$"
	case dialectSQLServer:
		marker = sqlServerPlaceholder
		prefix = "@p"
	default:
		return clause
	}
	return marker.ReplaceAllStringFunc(clause, func(raw string) string {
		value, err := strconv.Atoi(strings.TrimPrefix(raw, prefix))
		if err != nil {
			panic(fmt.Sprintf("offset SQL placeholder %q: %v", raw, err))
		}
		return prefix + strconv.Itoa(value+offset)
	})
}

// likeEscapeChar is "!" rather than "\" because MySQL's NO_BACKSLASH_ESCAPES
// mode and ClickHouse's fixed backslash handling disagree about the latter.
const likeEscapeChar = "!"

// likeMatch renders a case-insensitive substring predicate against an already
// quoted column, and returns the pattern to bind for needle. ILIKE is
// postgres-only, so every dialect folds case explicitly instead.
func (d sqlDialect) likeMatch(quotedColumn, needle string) (string, string) {
	if d == dialectClickHouse {
		// ClickHouse's LIKE takes no ESCAPE clause, so its metacharacters are
		// escaped with the backslash it does understand.
		return fmt.Sprintf("lower(%s) LIKE ?", quotedColumn), "%" + escapeLikeNeedle(needle, `\`, d) + "%"
	}
	return fmt.Sprintf("LOWER(%s) LIKE ? ESCAPE '%s'", quotedColumn, likeEscapeChar),
		"%" + escapeLikeNeedle(needle, likeEscapeChar, d) + "%"
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
