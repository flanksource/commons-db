package providers

import (
	"fmt"
	"regexp"
	"strings"
)

// sqlSpan is one region of a statement that a wrapper must reason about: either
// bare SQL or a quoted/commented region whose contents mean nothing.
type sqlSpan struct {
	start   int
	end     int
	literal bool
}

// scanSQL splits statement into bare and non-bare spans, walking past
// single-quoted strings, dollar-quoted bodies, double-quoted and bracketed
// identifiers, and line and block comments.
//
// It is deliberately not a parser: it answers "is this text real SQL or the
// inside of a literal" and nothing about what the statement means. That is
// enough for the two questions a wrapper actually has — where the author's WITH
// clause ends, and whether a bind marker is real.
func scanSQL(statement string) []sqlSpan {
	spans := []sqlSpan{}
	bareFrom := 0
	appendBare := func(upto int) {
		if upto > bareFrom {
			spans = append(spans, sqlSpan{start: bareFrom, end: upto})
		}
	}
	for i := 0; i < len(statement); {
		start := i
		var end int
		switch {
		case strings.HasPrefix(statement[i:], "--"):
			end = indexAfter(statement, i+2, "\n")
		case strings.HasPrefix(statement[i:], "/*"):
			end = indexAfter(statement, i+2, "*/")
		case statement[i] == '\'':
			end = closingQuote(statement, i, '\'')
		case statement[i] == '"':
			end = closingQuote(statement, i, '"')
		case statement[i] == '[':
			end = indexAfter(statement, i+1, "]")
		case statement[i] == '$':
			tag := dollarTag(statement, i)
			if tag == "" {
				i++
				continue
			}
			end = indexAfter(statement, i+len(tag), tag)
		default:
			i++
			continue
		}
		appendBare(start)
		spans = append(spans, sqlSpan{start: start, end: end, literal: true})
		i, bareFrom = end, end
	}
	appendBare(len(statement))
	return spans
}

// closingQuote finds the end of a quoted region, treating a doubled quote as an
// escaped one rather than a close followed by an open.
func closingQuote(statement string, from int, quote byte) int {
	for i := from + 1; i < len(statement); i++ {
		if statement[i] != quote {
			continue
		}
		if i+1 < len(statement) && statement[i+1] == quote {
			i++
			continue
		}
		return i + 1
	}
	return len(statement)
}

// dollarTag reads a postgres dollar-quote opener ($$ or $tag$) at from,
// returning "" when the "$" is something else — a placeholder, say.
func dollarTag(statement string, from int) string {
	for i := from + 1; i < len(statement); i++ {
		char := statement[i]
		if char == '$' {
			return statement[from : i+1]
		}
		if char != '_' && !isAlphaNumeric(char) {
			return ""
		}
	}
	return ""
}

func indexAfter(statement string, from int, needle string) int {
	if at := strings.Index(statement[from:], needle); at >= 0 {
		return from + at + len(needle)
	}
	return len(statement)
}

func isAlphaNumeric(char byte) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9')
}

var (
	leadingWith      = regexp.MustCompile(`(?is)^\s*with\s+(recursive\s+)?`)
	cteNameAtCursor  = regexp.MustCompile(`(?is)^\s*("[^"]+"|` + "`" + `[^` + "`" + `]+` + "`" + `|\[[^\]]+\]|[A-Za-z_][A-Za-z0-9_$]*)\s*(\([^)]*\)\s*)?as\s*(?:materialized\s*|not\s+materialized\s*)?\(`)
	postgresBindMark = regexp.MustCompile(`\$[0-9]+`)
	sqlServerBindMk  = regexp.MustCompile(`(?i)@p[0-9]+`)
	clickHouseNumMk  = regexp.MustCompile(`\$[0-9]+`)
	clickHousePosMk  = regexp.MustCompile(`[^\\][?]`)
)

// splitWithPrefix separates a leading WITH clause from the statement it
// introduces, so an author's CTEs can be hoisted beside the wrapper's own
// rather than nested inside them.
//
// Nesting is not an option: SQL Server rejects a WITH inside a CTE body
// outright, and hoisting is legal everywhere, so there is one shape to build
// and one shape to test. Returns ("", statement, nil) when there is no WITH.
func splitWithPrefix(statement string) (prefix, body string, cteNames []string, err error) {
	head := leadingWith.FindStringIndex(statement)
	if head == nil {
		return "", statement, nil, nil
	}
	// A WITH inside a literal or a comment is not a CTE list.
	if !isBareOffset(statement, head[0]) {
		return "", statement, nil, nil
	}

	cursor := head[1]
	for {
		match := cteNameAtCursor.FindStringSubmatchIndex(statement[cursor:])
		if match == nil {
			return "", "", nil, fmt.Errorf(
				"could not read the WITH clause at offset %d; column filters wrap a query by hoisting its CTEs and need to know where they end", cursor)
		}
		cteNames = append(cteNames, strings.Trim(statement[cursor+match[2]:cursor+match[3]], "\"`[]"))
		bodyEnd := matchingParen(statement, cursor+match[1]-1)
		if bodyEnd < 0 {
			return "", "", nil, fmt.Errorf("the WITH clause has an unclosed CTE body")
		}
		cursor = bodyEnd
		rest := strings.TrimLeft(statement[cursor:], " \t\r\n")
		if strings.HasPrefix(rest, ",") {
			cursor = len(statement) - len(rest) + 1
			continue
		}
		break
	}
	return strings.TrimSpace(statement[:cursor]), strings.TrimSpace(statement[cursor:]), cteNames, nil
}

// matchingParen returns the offset just past the ")" closing the "(" at open,
// ignoring parentheses inside literals and comments.
func matchingParen(statement string, open int) int {
	depth := 0
	for _, span := range scanSQL(statement) {
		if span.literal || span.end <= open {
			continue
		}
		for i := max(span.start, open); i < span.end; i++ {
			switch statement[i] {
			case '(':
				depth++
			case ')':
				if depth--; depth == 0 {
					return i + 1
				}
			}
		}
	}
	return -1
}

func isBareOffset(statement string, offset int) bool {
	for _, span := range scanSQL(statement) {
		if offset >= span.start && offset < span.end {
			return !span.literal
		}
	}
	return true
}

// assertNoPlaceholders refuses an author statement that already carries a bind
// placeholder.
//
// Drivers number placeholders across the whole statement, so the args the
// wrapper is about to supply would bind to the author's marker instead of to
// the wrapper's — and a query that today fails cleanly with "there is no
// parameter $1" would start returning confidently wrong rows.
func assertNoPlaceholders(dialect sqlDialect, statement string) error {
	// ClickHouse substitutes client-side by regex over the raw text with no
	// idea which markers are inside a literal, so its guard reads the raw text
	// too — anything else would disagree with the driver about what a marker is.
	if dialect == dialectClickHouse {
		if clickHouseNumMk.MatchString(statement) || clickHousePosMk.MatchString(" "+statement) {
			return placeholderError(statement)
		}
		return nil
	}
	for _, span := range scanSQL(statement) {
		if span.literal {
			continue
		}
		text := statement[span.start:span.end]
		switch dialect {
		case dialectPostgres:
			// A bare "?" is left alone: in postgres it is a jsonb operator, and
			// the author's statement never passes through a placeholder rewriter.
			if postgresBindMark.MatchString(text) {
				return placeholderError(statement)
			}
		case dialectSQLServer:
			if sqlServerBindMk.MatchString(text) || strings.Contains(text, "?") {
				return placeholderError(statement)
			}
		default:
			if strings.Contains(text, "?") {
				return placeholderError(statement)
			}
		}
	}
	return nil
}

func placeholderError(string) error {
	return fmt.Errorf(
		"the query already contains a bind placeholder, which a column filter's own values would be numbered against; inline the value or remove the placeholder to filter this query")
}
