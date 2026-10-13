// The text a statement's reruns share: what keys an N+1, the same query run
// again and again with other values.

package sqlstatements

import (
	"fmt"
	"hash/fnv"
	"strings"
	"unicode"
)

// RepeatKey keys statement by its normalized text (NormalizeSQL), so the
// statements that differ only by their values or spacing share it: 16 hex
// digits of its FNV-1a hash, short enough to store with every row.
func RepeatKey(statement string) string {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(NormalizeSQL(statement)))
	return fmt.Sprintf("%016x", hash.Sum64())
}

// NormalizeSQL is statement with each string and number literal replaced by
// ?, its comments dropped and each run of whitespace collapsed to one space.
// Identifiers, quoted ones included, and bind placeholders ($1, @p1, ?) are
// kept, so two statements normalize alike exactly when they differ only by
// the values written into them.
func NormalizeSQL(statement string) string {
	var out strings.Builder
	spaced := false
	write := func(token string) {
		if spaced && out.Len() > 0 {
			out.WriteByte(' ')
		}
		spaced = false
		out.WriteString(token)
	}
	r := []rune(statement)
	for i := 0; i < len(r); {
		c := r[i]
		switch {
		case unicode.IsSpace(c):
			spaced, i = true, i+1
		case c == '-' && i+1 < len(r) && r[i+1] == '-':
			spaced, i = true, skipLineComment(r, i)
		case c == '/' && i+1 < len(r) && r[i+1] == '*':
			spaced, i = true, skipBlockComment(r, i)
		case c == '\'':
			i = closingQuote(r, i, '\'')
			write("?")
		case c == '"' || c == '`' || c == '[':
			end := closingQuote(r, i, map[rune]rune{'"': '"', '`': '`', '[': ']'}[c])
			write(string(r[i:end]))
			i = end
		case unicode.IsDigit(c):
			i = identifierEnd(r, i, true)
			write("?")
		case identifierStart(c):
			end := identifierEnd(r, i, false)
			write(string(r[i:end]))
			i = end
		default:
			write(string(c))
			i++
		}
	}
	return out.String()
}

func skipLineComment(r []rune, i int) int {
	for i < len(r) && r[i] != '\n' {
		i++
	}
	return i
}

func skipBlockComment(r []rune, i int) int {
	for i += 2; i < len(r); i++ {
		if r[i] == '*' && i+1 < len(r) && r[i+1] == '/' {
			return i + 2
		}
	}
	return i
}

// closingQuote is the index just past the quote closing the one opened at i,
// a doubled quote inside escaping itself; an unclosed quote runs to the end.
func closingQuote(r []rune, i int, closing rune) int {
	for i++; i < len(r); i++ {
		if r[i] != closing {
			continue
		}
		if i+1 < len(r) && r[i+1] == closing && closing != ']' {
			i++
			continue
		}
		return i + 1
	}
	return i
}

func identifierStart(c rune) bool {
	return unicode.IsLetter(c) || c == '_' || c == '$' || c == '@' || c == '#'
}

// identifierEnd is the index just past the identifier, or the number when
// number is set, starting at i. A number takes its decimal point and the
// letters of a hex or exponent form with it.
func identifierEnd(r []rune, i int, number bool) int {
	for i < len(r) && (identifierStart(r[i]) || unicode.IsDigit(r[i]) || (number && r[i] == '.')) {
		i++
	}
	return i
}
