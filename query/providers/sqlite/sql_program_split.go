package sqlite

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// sqlBatch is one batch of a SQL program: its text exactly as written, the
// lines it spans, and how many times `GO <count>` asks for it to run.
type sqlBatch struct {
	Index     int
	Text      string
	StartLine int
	EndLine   int
	Repeat    int
}

func (b sqlBatch) label() string {
	return fmt.Sprintf("batch %d (lines %d-%d)", b.Index, b.StartLine, b.EndLine)
}

// goSeparator is the sqlcmd batch separator: a line holding only GO, any
// case, with an optional repeat count and an optional trailing -- comment.
var goSeparator = regexp.MustCompile(`(?i)^[ \t]*GO(?:[ \t]+([0-9]+))?[ \t]*(?:--.*)?\r?$`)

// splitSQLBatches splits program at its GO lines, the sqlcmd convention.
//
// The rule is the same in every dialect: SQL Server's tools define GO, and no
// other supported dialect gives a line holding only GO any meaning — it is not
// a statement anywhere — so treating it as a separator there cannot change
// what a valid program means. What does differ per dialect is what counts as a
// string or a comment, and a GO inside either is text, never a separator.
//
// separated reports whether any GO line was found; a program without one is a
// single batch whose text is program itself. Batches holding only whitespace
// and comments are dropped.
func splitSQLBatches(dialect sqlDialect, program string) (batches []sqlBatch, separated bool, err error) {
	src := []rune(program)
	lex := sqlLexer{dialect: dialect}
	start, startLine, line := 0, 1, 1
	for i := 0; i <= len(src); {
		if lex.state == lexNormal && (i == 0 || src[i-1] == '\n') {
			end := lineEnd(src, i)
			if match := goSeparator.FindStringSubmatch(string(src[i:end])); match != nil {
				repeat, err := batchRepeat(match[1], line)
				if err != nil {
					return nil, false, err
				}
				batches = appendSQLBatch(batches, sqlBatchSpan{src[start:i], startLine, lex.code, repeat})
				separated, lex.code = true, false
				start, startLine, i = min(end+1, len(src)), line+1, end+1
				line++
				continue
			}
		}
		if i == len(src) {
			break
		}
		consumed := lex.step(src, i)
		line += strings.Count(string(src[i:i+consumed]), "\n")
		i += consumed
	}
	if !separated {
		return []sqlBatch{{Index: 1, Text: program, StartLine: 1, EndLine: line, Repeat: 1}}, false, nil
	}
	if start < len(src) {
		batches = appendSQLBatch(batches, sqlBatchSpan{src[start:], startLine, lex.code, 1})
	}
	if len(batches) == 0 {
		return nil, true, fmt.Errorf("the SQL program holds no statements, only GO separators")
	}
	return batches, true, nil
}

func batchRepeat(count string, line int) (int, error) {
	if count == "" {
		return 1, nil
	}
	repeat, err := strconv.Atoi(count)
	if err != nil || repeat < 1 {
		return 0, fmt.Errorf("line %d: GO %s must repeat its batch a positive number of times", line, count)
	}
	return repeat, nil
}

// sqlBatchSpan is a batch's text as read, before it is numbered and trimmed to
// the lines that hold something.
type sqlBatchSpan struct {
	text      []rune
	firstLine int
	code      bool
	repeat    int
}

func appendSQLBatch(batches []sqlBatch, span sqlBatchSpan) []sqlBatch {
	if !span.code {
		return batches
	}
	text := string(span.text)
	leading := len(text) - len(strings.TrimLeftFunc(text, unicode.IsSpace))
	body := strings.TrimRightFunc(text, unicode.IsSpace)
	startLine := span.firstLine + strings.Count(text[:leading], "\n")
	endLine := startLine + strings.Count(body[leading:], "\n")
	return append(batches, sqlBatch{
		Index: len(batches) + 1, Text: text, StartLine: startLine, EndLine: endLine, Repeat: span.repeat,
	})
}

func lineEnd(src []rune, from int) int {
	for i := from; i < len(src); i++ {
		if src[i] == '\n' {
			return i
		}
	}
	return len(src)
}

type lexState int

const (
	lexNormal lexState = iota
	lexQuoted
	lexLineComment
	lexBlockComment
	lexDollarQuoted
)

// sqlLexer knows just enough of a dialect to tell a string, a quoted name or
// a comment from code, which is all a batch split needs.
type sqlLexer struct {
	dialect sqlDialect
	state   lexState
	// closing is the rune that ends a quoted string or name.
	closing rune
	// tag is the delimiter of a postgres dollar-quoted string.
	tag   string
	depth int
	// code reports that the batch so far holds something besides whitespace
	// and comments.
	code bool
}

var dollarTag = regexp.MustCompile(`^\$[A-Za-z_][A-Za-z0-9_]*\$|^\$\$`)

// step consumes the token at src[i] and reports how many runes it took.
func (l *sqlLexer) step(src []rune, i int) int {
	switch l.state {
	case lexQuoted:
		return l.stepQuoted(src, i)
	case lexLineComment:
		if src[i] == '\n' {
			l.state = lexNormal
		}
		return 1
	case lexBlockComment:
		return l.stepBlockComment(src, i)
	case lexDollarQuoted:
		if strings.HasPrefix(string(src[i:min(i+len([]rune(l.tag)), len(src))]), l.tag) {
			l.state = lexNormal
			return len([]rune(l.tag))
		}
		return 1
	}
	return l.stepNormal(src, i)
}

func (l *sqlLexer) stepNormal(src []rune, i int) int {
	r, next := src[i], peek(src, i+1)
	switch {
	case r == '-' && next == '-', r == '#' && l.dialect == dialectMySQL:
		l.state = lexLineComment
		return 1
	case r == '/' && next == '*':
		l.state, l.depth = lexBlockComment, 1
		return 2
	}
	if !unicode.IsSpace(r) {
		l.code = true
	}
	switch {
	case r == '\'' || r == '"' || r == '`':
		l.state, l.closing = lexQuoted, r
	case r == '[' && (l.dialect == dialectSQLServer || l.dialect == dialectSQLite):
		l.state, l.closing = lexQuoted, ']'
	case r == '$' && l.dialect == dialectPostgres:
		if tag := dollarTag.FindString(string(src[i:min(i+64, len(src))])); tag != "" {
			l.state, l.tag = lexDollarQuoted, tag
			return len([]rune(tag))
		}
	}
	return 1
}

func (l *sqlLexer) stepQuoted(src []rune, i int) int {
	r := src[i]
	backslashEscapes := l.dialect == dialectMySQL || l.dialect == dialectClickHouse
	if r == '\\' && backslashEscapes && l.closing != '`' {
		return min(2, len(src)-i)
	}
	if r != l.closing {
		return 1
	}
	if peek(src, i+1) == l.closing {
		return 2
	}
	l.state = lexNormal
	return 1
}

// stepBlockComment follows nesting where the dialect nests comments (T-SQL and
// postgres do; the others end at the first */).
func (l *sqlLexer) stepBlockComment(src []rune, i int) int {
	r, next := src[i], peek(src, i+1)
	nests := l.dialect == dialectSQLServer || l.dialect == dialectPostgres
	switch {
	case r == '/' && next == '*' && nests:
		l.depth++
		return 2
	case r == '*' && next == '/':
		l.depth--
		if l.depth == 0 || !nests {
			l.state = lexNormal
		}
		return 2
	}
	return 1
}

func peek(src []rune, i int) rune {
	if i < len(src) {
		return src[i]
	}
	return 0
}
