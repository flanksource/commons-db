package esdsl

import (
	"fmt"
	"regexp"
	"strings"
)

// maxRegexpLength bounds a regexp operand. OpenSearch compiles regexps into an
// automaton, so an unbounded pattern from a supplied parameter is a cheap way
// to burn cluster CPU.
const maxRegexpLength = 1000

// maxDeterminizedStates caps the automaton every compiled regexp expands to.
const maxDeterminizedStates = 10000

// luceneSpecial lists the characters that carry syntactic meaning inside a
// query_string / simple_query_string operand.
const luceneSpecial = `+-=&|><!(){}[]^"~*?:\/`

// fieldNamePattern accepts the field-name grammar OpenSearch itself accepts,
// including the wildcards used by _source and stored_fields ("*", "labels.*").
var fieldNamePattern = regexp.MustCompile(`^[A-Za-z_@*][A-Za-z0-9_@.\-*]*$`)

// EscapeLucene neutralises the query-string syntax in s so a supplied parameter
// is matched as text rather than interpreted as an operator.
func EscapeLucene(s string) string {
	var out strings.Builder
	out.Grow(len(s) + len(s)/4)
	for _, r := range s {
		if r < 128 && strings.ContainsRune(luceneSpecial, r) {
			out.WriteByte('\\')
		}
		out.WriteRune(r)
	}
	return out.String()
}

// ValidateFieldName rejects a field name that could break out of the JSON body
// it is emitted into. Field names always come from the specification, never
// from a supplied parameter, so this is a fail-fast authoring check.
func ValidateFieldName(name string) error {
	if name == "" {
		return fmt.Errorf("field name must not be empty")
	}
	if !fieldNamePattern.MatchString(name) {
		return fmt.Errorf("field name %q is not a valid OpenSearch field", name)
	}
	return nil
}

func validateFieldNames(names []string) error {
	for _, name := range names {
		if err := ValidateFieldName(name); err != nil {
			return err
		}
	}
	return nil
}

func validateRegexpOperand(pattern string) error {
	if len(pattern) > maxRegexpLength {
		return fmt.Errorf("regexp is %d characters, the limit is %d", len(pattern), maxRegexpLength)
	}
	return nil
}
