package sqlite

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/sqlglot-go/sqlglot"
)

// A SQL program is a query the provider runs as written instead of wrapping it
// for paging: anything but a single read-only query. That is decided by
// sqlProgram, deterministically, from the query's text:
//
//   - it holds a GO separator line (see splitSQLBatches), or
//   - the dialect's parser cannot read it as one statement (sqlglot.ParseOne
//     fails — several statements, EXEC on dialects it keeps as a command, …), or
//   - the one statement is not a query (Select, Union, Intersect, Except), or
//   - it is a query that writes (sqlglot.IsWrite: SELECT … INTO, a write in a
//     CTE).
//
// A program runs batch by batch, in order, on one session, so SET options and
// temp tables carry from one batch to the next. It is not wrapped in a
// transaction: each batch commits as its own statements dictate, as under
// sqlcmd, and a program that wants atomicity says BEGIN TRANSACTION itself.
// The first failing batch stops the run.
//
// Every batch's result sets are kept in order on the Page's Batches; the
// Page's Rows are the last result set that has columns. A program runs once,
// so paging, sorting, filtering and cursors are refused rather than ignored.

// pageableQueryClasses are the statement roots the paging wrapper can wrap.
var pageableQueryClasses = map[string]bool{"Select": true, "Union": true, "Intersect": true, "Except": true}

// sqlProgram splits the query into batches and reports whether it is a
// program; a pageable query is left to the paging path untouched.
func sqlProgram(dialect sqlDialect, statement string) ([]sqlBatch, bool, error) {
	batches, separated, err := splitSQLBatches(dialect, statement)
	if err != nil {
		return nil, false, err
	}
	if separated {
		return batches, true, nil
	}
	glot, err := dialect.glot()
	if err != nil {
		return nil, false, err
	}
	tree, err := sqlglot.ParseOne(statement, glot)
	if err == nil && pageableQueryClasses[tree.Class] && !sqlglot.IsWrite(tree) {
		return nil, false, nil
	}
	return batches, true, nil
}

// refuseProgramPaging fails a request that asks a program for something only
// a pageable query can give.
func refuseProgramPaging(req query.ProviderRequest, page query.PageRequest) error {
	asked := []string{}
	if filtersActive(req.Filters) {
		asked = append(asked, "column filters")
	}
	if len(req.Order) > 0 {
		asked = append(asked, "an order")
	}
	if !req.Position.IsZero() || page.Mode() == query.PagingCursor {
		asked = append(asked, "a cursor")
	}
	if page.Offset > 0 || !page.ReadAll {
		asked = append(asked, "a page")
	}
	if len(asked) == 0 {
		return nil
	}
	return fmt.Errorf("the query runs as a SQL program (it is not a single read-only SELECT), which runs once and cannot apply %s; read it whole with Execute",
		strings.Join(asked, ", "))
}

func filtersActive(filters []query.ColumnFilterValue) bool {
	for _, filter := range filters {
		if !filter.IsZero() {
			return true
		}
	}
	return false
}

// boundBatch is a batch with its params bound: Bound is the text the server
// receives (the dialect's bind markers in place of the template markers) and
// Args the values those markers carry.
type boundBatch struct {
	sqlBatch
	Bound string
	Args  []any
}

// bindProgram binds every batch before any runs, so a bind failure in a later
// batch runs nothing, and the request a program records is what it sends.
func bindProgram(dialect sqlDialect, req query.ProviderRequest, batches []sqlBatch) ([]boundBatch, error) {
	bound := make([]boundBatch, len(batches))
	for i, batch := range batches {
		text, args, err := bindSQLBatch(dialect, batch, sqlQuery{Text: req.Query, Args: req.QueryArgs, Identifiers: req.QueryIdentifiers})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", batch.label(), err)
		}
		bound[i] = boundBatch{sqlBatch: batch, Bound: text, Args: args}
	}
	return bound, nil
}

// programRequest is the statement and arguments a program records: its batches
// as sent, joined by GO, and — since each batch binds its own markers from 1 —
// the arguments only when there is a single batch; every batch's arguments are
// in the details.
func programRequest(batches []boundBatch) (string, []any, []map[string]any) {
	texts := make([]string, len(batches))
	details := make([]map[string]any, len(batches))
	for i, batch := range batches {
		texts[i] = strings.TrimSpace(batch.Bound)
		details[i] = map[string]any{"index": batch.Index, "startLine": batch.StartLine, "arguments": batch.Args}
	}
	var args []any
	if len(batches) == 1 {
		args = batches[0].Args
	}
	return strings.Join(texts, "\nGO\n"), args, details
}

// runSQLProgram runs every batch on one session and returns the single Page a
// program is. When the diagnostics want it, that session is recorded before the
// first batch and, past AutoDiagnose, sampled until the program ends.
func runSQLProgram(ctx context.Context, client *sql.DB, dialect sqlDialect, diagnostics *query.ProviderDiagnostics, batches []boundBatch) (query.Page, error) {
	conn, err := client.Conn(ctx)
	if err != nil {
		return query.Page{}, fmt.Errorf("open a session for the SQL program: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if diagnostics.WantsSession() {
		session, err := recordSession(ctx, conn, sessionRequest{Dialect: dialect, Diagnostics: diagnostics})
		if err != nil {
			return query.Page{}, err
		}
		defer startAutoDiagnose(ctx, client, session, diagnostics)()
	}
	observe := query.BatchObserver(ctx)
	var results []query.BatchResult
	for _, batch := range batches {
		text, args := batch.Bound, batch.Args
		for run := 1; run <= batch.Repeat; run++ {
			event := query.BatchEvent{Batch: batch.Index, Run: run, StartLine: batch.StartLine}
			notice := func(message string) {
				if observe != nil {
					event.Message = message
					observe(event)
				}
			}
			result, err := runSQLBatch(ctx, conn, dialect, text, args, notice)
			if err != nil {
				return query.Page{}, fmt.Errorf("%s: %w", batch.label(), err)
			}
			result.Index, result.Run, result.StartLine, result.EndLine = batch.Index, run, batch.StartLine, batch.EndLine
			results = append(results, result)
			if observe != nil {
				observe(query.BatchEvent{Batch: batch.Index, Run: run, StartLine: batch.StartLine, Done: true, RowsAffected: result.RowsAffected})
			}
		}
	}
	return query.Page{Rows: query.LastResultSet(results), Batches: results}, nil
}

// runSQLBatch runs one batch's text and reads every result set it returns,
// handing each server message to notice as it arrives where the driver
// surfaces them.
func runSQLBatch(ctx context.Context, conn *sql.Conn, dialect sqlDialect, text string, args []any, notice func(string)) (query.BatchResult, error) {
	if dialect == dialectSQLServer {
		return runSQLServerBatch(ctx, conn, text, args, notice)
	}
	var before int64
	if dialect == dialectSQLite {
		if err := conn.QueryRowContext(ctx, "SELECT total_changes()").Scan(&before); err != nil {
			return query.BatchResult{}, fmt.Errorf("read sqlite total_changes: %w", err)
		}
	}
	rows, err := conn.QueryContext(ctx, text, driverArgs(dialect, args)...)
	if err != nil {
		return query.BatchResult{}, err
	}
	sets, err := readResultSets(rows)
	if err != nil {
		return query.BatchResult{}, err
	}
	result := query.BatchResult{ResultSets: sets}
	if dialect == dialectSQLite {
		var after int64
		if err := conn.QueryRowContext(ctx, "SELECT total_changes()").Scan(&after); err != nil {
			return query.BatchResult{}, fmt.Errorf("read sqlite total_changes: %w", err)
		}
		result.RowsAffected = new(after - before)
	}
	return result, nil
}

// readResultSets reads every result set of rows and closes it. A result set
// without columns — a write's — carries nothing to keep.
func readResultSets(rows *sql.Rows) ([][]query.Row, error) {
	defer func() { _ = rows.Close() }()
	var sets [][]query.Row
	for {
		set, hasColumns, err := readResultSet(rows)
		if err != nil {
			return nil, err
		}
		if hasColumns {
			sets = append(sets, set)
		}
		if !rows.NextResultSet() {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return sets, rows.Close()
}

func readResultSet(rows *sql.Rows) ([]query.Row, bool, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, false, fmt.Errorf("describe result set: %w", err)
	}
	if len(columns) == 0 {
		for rows.Next() {
		}
		return nil, false, rows.Err()
	}
	scanner, err := db.NewRowScanner(rows)
	if err != nil {
		return nil, false, err
	}
	set := []query.Row{}
	for scanner.Next() {
		set = append(set, query.Row(scanner.Row()))
	}
	return set, true, scanner.Err()
}

// definitionBatch matches a batch that defines a routine or a view: its text
// is stored by the server as written, so it is never rewritten.
var definitionBatch = regexp.MustCompile(`(?i)^(CREATE|ALTER)( OR (ALTER|REPLACE))?( TEMP| TEMPORARY)? (PROC|PROCEDURE|FUNCTION|TRIGGER|VIEW)\b`)

// bindSQLBatch replaces the param and identifier markers in one batch with the
// dialect's bind markers and quoted names, and returns the batch's args in the
// order its markers number them. A batch without markers runs as written.
func bindSQLBatch(dialect sqlDialect, batch sqlBatch, program sqlQuery) (string, []any, error) {
	if !strings.Contains(batch.Text, sqlParamMarkerPrefix) && !strings.Contains(batch.Text, sqlIdentifierMarkerPrefix) {
		return batch.Text, nil, nil
	}
	glot, err := dialect.glot()
	if err != nil {
		return "", nil, err
	}
	tokens, err := sqlTokens(glot, batch.Text)
	if err != nil {
		return "", nil, fmt.Errorf("cannot be tokenized as %s: %w", glot, err)
	}
	if definitionBatch.MatchString(tokenPrefix(tokens, 6)) {
		return "", nil, fmt.Errorf("a routine or view definition is stored as written, so a params reference cannot be bound inside it; pass the value when the routine is called")
	}
	text := batch.Text
	var args []any
	for index, value := range program.Args {
		marker := sqlParamMarker(index)
		if !strings.Contains(text, marker) {
			continue
		}
		if dialect == dialectSQLite || dialect == dialectMySQL {
			if err := refuseAuthorPlaceholders(tokens); err != nil {
				return "", nil, err
			}
		}
		args = append(args, value)
		text = strings.Replace(text, marker, programBind(dialect, len(args)), 1)
	}
	for index, path := range program.Identifiers {
		quoted, err := quotedIdentifierPath(glot, path)
		if err != nil {
			return "", nil, err
		}
		text = strings.Replace(text, sqlIdentifierMarker(index), quoted, 1)
	}
	return text, args, nil
}

// programBind is the marker the dialect's driver binds the n-th arg of a
// statement to. SQLite's ?N names its arg, so it binds correctly in every
// statement of a multi-statement batch.
func programBind(dialect sqlDialect, n int) string {
	switch dialect {
	case dialectPostgres:
		return "$" + strconv.Itoa(n)
	case dialectSQLServer:
		return "@p" + strconv.Itoa(n)
	case dialectClickHouse:
		return "@" + clickHouseBindName(n)
	case dialectSQLite:
		return "?" + strconv.Itoa(n)
	default:
		return "?"
	}
}

// refuseAuthorPlaceholders refuses a bare ? the author wrote beside params: it
// would take an arg meant for a params reference.
func refuseAuthorPlaceholders(tokens []sqlglot.Token) error {
	for _, token := range tokens {
		if token.Type == sqlglot.TokPLACEHOLDER {
			return fmt.Errorf("the query holds a bind placeholder %q beside params references, which would take one of their values; use params only", token.Text)
		}
	}
	return nil
}

func quotedIdentifierPath(glot, path string) (string, error) {
	parts, err := validateSQLIdentifierPath(path)
	if err != nil {
		return "", err
	}
	quoted := make([]string, len(parts))
	for index, part := range parts {
		if quoted[index], err = sqlglot.Generate(identifier(part), glot); err != nil {
			return "", fmt.Errorf("write identifier %q: %w", path, err)
		}
	}
	return strings.Join(quoted, "."), nil
}

func tokenPrefix(tokens []sqlglot.Token, count int) string {
	words := make([]string, 0, count)
	for _, token := range tokens[:min(count, len(tokens))] {
		words = append(words, strings.ToUpper(token.Text))
	}
	return strings.Join(words, " ")
}

// sqlTokens tokenizes text in glot, reading a command's payload as tokens too.
//
// The tokenizer keeps the rest of a statement opening with a command keyword
// — T-SQL's EXEC, among others — as one string token, so a params marker in
// `EXEC dbo.proc @X = …` would read as text inside a literal. The payload is
// the statement as written, so tokenizing it again yields what it holds: a
// marker that was bare stays bare, one inside a real literal stays a string.
func sqlTokens(glot, text string) ([]sqlglot.Token, error) {
	tokenizer, err := sqlglot.NewTokenizer(glot)
	if err != nil {
		return nil, err
	}
	tokens, err := tokenizer.Tokenize(text)
	if err != nil {
		return nil, err
	}
	cfg := tokenizer.Config()
	expanded := make([]sqlglot.Token, 0, len(tokens))
	for index, token := range tokens {
		if token.Type != sqlglot.TokSTRING || index == 0 || !commandPayload(cfg, tokens, index) {
			expanded = append(expanded, token)
			continue
		}
		inner, err := sqlTokens(glot, token.Text)
		if err != nil {
			return nil, err
		}
		expanded = append(expanded, inner...)
	}
	return expanded, nil
}

// commandPayload reports whether tokens[index] is the payload the tokenizer
// swallowed after a command keyword, which it does only where a command can
// start a statement.
func commandPayload(cfg *sqlglot.Config, tokens []sqlglot.Token, index int) bool {
	if _, ok := cfg.Commands[tokens[index-1].Type]; !ok {
		return false
	}
	if index == 1 {
		return true
	}
	_, ok := cfg.CommandPrefixTokens[tokens[index-2].Type]
	return ok
}
