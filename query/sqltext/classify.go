package sqltext

import "strings"

const (
	StmtSelect StatementType = "SELECT"
	StmtInsert StatementType = "INSERT"
	StmtUpdate StatementType = "UPDATE"
	StmtDelete StatementType = "DELETE"
	StmtMerge  StatementType = "MERGE"
	StmtExec   StatementType = "EXEC"
	StmtDDL    StatementType = "DDL"
	StmtOther  StatementType = "OTHER"
)

// StatementTypes is every StatementType ClassifyStatement can return.
var StatementTypes = []StatementType{StmtSelect, StmtInsert, StmtUpdate, StmtDelete, StmtMerge, StmtExec, StmtDDL, StmtOther}

// ddlKeywords are the leading verbs of a schema change. Only the statement's
// first token is checked, so a procedure body (`CREATE PROCEDURE p AS SELECT …`)
// is DDL while `SELECT … INTO #t` stays SELECT.
var ddlKeywords = map[string]struct{}{
	"CREATE":   {},
	"ALTER":    {},
	"DROP":     {},
	"TRUNCATE": {},
}

// classKeywords maps a leading SQL verb to its StatementType. EXEC/EXECUTE are
// included so a stored-procedure call carries a type token instead of falling
// through to StmtOther — which matters wherever an application reaches its data
// through procedures rather than statements.
var classKeywords = map[string]StatementType{
	"SELECT":  StmtSelect,
	"INSERT":  StmtInsert,
	"UPDATE":  StmtUpdate,
	"DELETE":  StmtDelete,
	"MERGE":   StmtMerge,
	"EXEC":    StmtExec,
	"EXECUTE": StmtExec,
}

// tableKeywords are the keywords whose following identifier names a table/object.
// USING names the MERGE source table (SQL Server only uses USING in MERGE — it
// has no JOIN…USING(col) syntax — so it never mis-fires on a join condition).
// EXEC/EXECUTE name a stored procedure: it is an object reference, not a table,
// but it is the only object the call text exposes, so it belongs in the same
// token set that `--table` matches against. TABLE names the table a
// CREATE/ALTER/DROP/TRUNCATE TABLE changes.
var tableKeywords = map[string]struct{}{
	"FROM":    {},
	"JOIN":    {},
	"INTO":    {},
	"UPDATE":  {},
	"MERGE":   {},
	"USING":   {},
	"EXEC":    {},
	"EXECUTE": {},
	"TABLE":   {},
}

// listStopKeywords end a comma-separated FROM list, so we stop expecting more
// tables after one of these appears.
var listStopKeywords = map[string]struct{}{
	"WHERE": {}, "GROUP": {}, "ORDER": {}, "HAVING": {}, "ON": {},
	"UNION": {}, "SET": {}, "VALUES": {}, "OPTION": {}, "WITH": {},
	"INNER": {}, "LEFT": {}, "RIGHT": {}, "FULL": {}, "CROSS": {}, "OUTER": {},
}

// ClassifyStatement returns the StatementType of a SQL statement by finding the
// first DML verb at parenthesis depth 0. This naturally resolves CTEs
// (`WITH x AS (SELECT …) SELECT …` → SELECT, since the inner SELECT is nested)
// and `INSERT … SELECT` (→ INSERT, the first top-level verb), and skips leading
// comments/parens via the tokenizer. A leading DDL verb wins before that scan,
// since a DDL body may itself contain DML. Unrecognized leading verbs →
// StmtOther.
func ClassifyStatement(sql string) StatementType {
	tokens := tokenizeSQL(sql)
	if len(tokens) > 0 {
		if _, ok := ddlKeywords[strings.ToUpper(tokens[0])]; ok {
			return StmtDDL
		}
	}
	depth := 0
	for _, tok := range tokens {
		switch tok {
		case "(":
			depth++
			continue
		case ")":
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth != 0 {
			continue
		}
		if t, ok := classKeywords[strings.ToUpper(tok)]; ok {
			return t
		}
	}
	return StmtOther
}

// ExtractTables returns the distinct table/object names referenced after FROM /
// JOIN / INTO / UPDATE / MERGE / USING / EXEC keywords. It is a best-effort parse
// of the SQL text — the only structured source XE exposes for a completed
// statement.
//
// For a stored-procedure call the extracted name is the PROCEDURE, not the
// tables its body touches: the body never appears in the trace text. So a table
// filter of `usp_GetOrderTotals` matches the call while one naming a table the
// body reads does not. Capturing sp_statement_completed surfaces the inner
// statements, and those do carry their own table names.
//
// A FROM clause is a comma-separated list of `table [AS] [alias] [hints]` refs,
// so once a FROM opens a list the parser keeps it open across alias/hint tokens
// and reopens table-expectation on each top-level comma — `FROM A a, B b` yields
// both A and B, not just A. List state is tracked PER parenthesis depth so a
// subquery's own column commas (`(SELECT a, b FROM X)`) never reopen the outer
// list, while the subquery's inner FROM is still scanned. A keyword in table
// position (e.g. MERGE's `UPDATE SET`) ends the clause instead of being taken as
// a table name. Names are schema- and bracket-stripped and de-duplicated
// case-insensitively, preserving the first-seen casing; an exotic shape (dynamic
// SQL, table-valued functions in a comma list) may still under-extract.
func ExtractTables(sql string) []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(tok string) {
		name := cleanTableName(tok)
		if name == "" {
			return
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, name)
	}

	expect := false          // next identifier (at this depth) names a table
	depth := 0               // current parenthesis nesting depth
	listAt := map[int]bool{} // depth -> a FROM comma list is open at that depth
	for _, tok := range tokenizeSQL(sql) {
		switch tok {
		case "(":
			// A "(" in table position is a derived table / subquery: don't take
			// it as a name; its inner FROM is scanned at the deeper depth.
			expect = false
			depth++
			continue
		case ")":
			delete(listAt, depth) // a list opened inside these parens closes with them
			if depth > 0 {
				depth--
			}
			continue
		}
		up := strings.ToUpper(tok)
		if _, isTableKw := tableKeywords[up]; isTableKw {
			expect = true
			listAt[depth] = up == "FROM"
			continue
		}
		if expect {
			// A stop keyword in table position is not a table name (e.g. MERGE's
			// `UPDATE SET …` — UPDATE expects a target, but SET ends the clause).
			if _, stop := listStopKeywords[up]; stop {
				expect = false
				listAt[depth] = false
				continue
			}
			// `RETURNS TABLE AS …`: no name follows TABLE.
			if up == "AS" {
				expect = false
				continue
			}
			add(tok)
			expect = false
			continue
		}
		if tok == "," {
			if listAt[depth] { // a top-level comma in a FROM list names another table
				expect = true
			}
			continue
		}
		if _, stop := listStopKeywords[up]; stop {
			listAt[depth] = false
		}
	}
	return out
}

// tokenizeSQL splits SQL into a stream of tokens for classification and table
// extraction. Parentheses and commas are emitted as single-char tokens; string
// literals and comments are dropped; qualified/bracketed/quoted identifiers
// (`[dbo].[AsActivity]`, `dbo.AsActivity`, `"x"`) collapse to one token.
func tokenizeSQL(sql string) []string {
	var toks []string
	r := []rune(sql)
	n := len(r)
	for i := 0; i < n; {
		c := r[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '-' && i+1 < n && r[i+1] == '-':
			for i < n && r[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && r[i+1] == '*':
			i += 2
			for i+1 < n && (r[i] != '*' || r[i+1] != '/') {
				i++
			}
			i += 2
		case c == '\'':
			i++
			for i < n {
				if r[i] == '\'' {
					if i+1 < n && r[i+1] == '\'' { // '' escape
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case c == '(' || c == ')' || c == ',':
			toks = append(toks, string(c))
			i++
		case isIdentStart(c):
			tok, next := readIdentifier(r, i)
			toks = append(toks, tok)
			i = next
		default:
			i++
		}
	}
	return toks
}

// readIdentifier reads a possibly-qualified identifier starting at i: a chain of
// bracketed (`[…]`), quoted (`"…"`), or bare identifier parts joined by `.`.
func readIdentifier(r []rune, i int) (string, int) {
	n := len(r)
	var b strings.Builder
	for i < n {
		switch {
		case r[i] == '[':
			for i < n {
				b.WriteRune(r[i])
				if r[i] == ']' {
					i++
					break
				}
				i++
			}
		case r[i] == '"':
			b.WriteRune(r[i])
			i++
			for i < n {
				b.WriteRune(r[i])
				if r[i] == '"' {
					i++
					break
				}
				i++
			}
		case isIdentPart(r[i]):
			for i < n && isIdentPart(r[i]) {
				b.WriteRune(r[i])
				i++
			}
		default:
			return b.String(), i
		}
		if i < n && r[i] == '.' {
			b.WriteRune('.')
			i++
			continue
		}
		return b.String(), i
	}
	return b.String(), i
}

func isIdentStart(c rune) bool {
	return c == '[' || c == '"' || isIdentPart(c)
}

func isIdentPart(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '_' || c == '#' || c == '$' || c == '@'
}

// cleanTableName reduces a qualified identifier token to the bare table name:
// it takes the last dot-separated part (honoring brackets/quotes) and strips
// surrounding [] or "" delimiters.
func cleanTableName(tok string) string {
	part := lastQualifiedPart(tok)
	part = strings.TrimSpace(part)
	if len(part) >= 2 {
		if part[0] == '[' && part[len(part)-1] == ']' {
			return strings.TrimSpace(part[1 : len(part)-1])
		}
		if part[0] == '"' && part[len(part)-1] == '"' {
			return strings.TrimSpace(part[1 : len(part)-1])
		}
	}
	return part
}

// lastQualifiedPart returns the final `.`-separated component of a qualified
// identifier, ignoring dots that fall inside [] or "" delimiters.
func lastQualifiedPart(tok string) string {
	depth := 0
	inQuote := false
	last := 0
	for i := 0; i < len(tok); i++ {
		switch tok[i] {
		case '[':
			depth++
		case ']':
			if depth > 0 {
				depth--
			}
		case '"':
			inQuote = !inQuote
		case '.':
			if depth == 0 && !inQuote {
				last = i + 1
			}
		}
	}
	return tok[last:]
}
