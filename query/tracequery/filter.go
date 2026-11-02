package tracequery

import (
	"fmt"
	"regexp"
	"strings"
)

// Mode includes or excludes a filter value.
type Mode string

const (
	Include Mode = "include"
	Exclude Mode = "exclude"
)

// Modes are a filter's values, each included or excluded: a trace passes
// when it has one of the included values (or none is included) and none of
// the excluded ones.
type Modes map[string]Mode

func (Modes) JSONSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string", "enum": []string{string(Include), string(Exclude)}}}
}

func (m Modes) validate(name string, value func(string) error) error {
	for key, mode := range m {
		if mode != Include && mode != Exclude {
			return fmt.Errorf("%s %q: mode must be include or exclude, not %q", name, key, mode)
		}
		if err := value(key); err != nil {
			return fmt.Errorf("%s %q: %w", name, key, err)
		}
	}
	return nil
}

func (m Modes) values(mode Mode) []string {
	result := []string{}
	for key, value := range m {
		if value == mode {
			result = append(result, key)
		}
	}
	return result
}

// pass reports whether a trace, holding the values has reports, passes m.
func (m Modes) pass(has func(string) bool) bool {
	included := m.values(Include)
	matched := len(included) == 0
	for _, value := range included {
		matched = matched || has(value)
	}
	for _, value := range m.values(Exclude) {
		if has(value) {
			return false
		}
	}
	return matched
}

// passAttributes reports whether spans pass attribute filters: included
// expressions are any-of per key and all-of across keys, and any span
// matching an excluded expression rejects the trace.
func (m Modes) passAttributes(spans []Span) bool {
	byKey := map[string][]string{}
	for _, expr := range m.values(Include) {
		key, _, _ := strings.Cut(expr, "=")
		byKey[key] = append(byKey[key], expr)
	}
	anySpan := func(expr string) bool {
		for _, span := range spans {
			if attributeMatches(span, expr) {
				return true
			}
		}
		return false
	}
	for _, exprs := range byKey {
		matched := false
		for _, expr := range exprs {
			matched = matched || anySpan(expr)
		}
		if !matched {
			return false
		}
	}
	for _, expr := range m.values(Exclude) {
		if anySpan(expr) {
			return false
		}
	}
	return true
}

func validAttribute(expr string) error {
	key, _, _ := strings.Cut(expr, "=")
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("an attribute filter is key, key=value or key=prefix*")
	}
	return nil
}

func validStatus(status string) error {
	if status != "ok" && status != "error" {
		return fmt.Errorf("status must be error or ok")
	}
	return nil
}

// attributeMatches reports whether span has the attribute expr names: a bare
// key matches its presence, key=value its value, with * as a wildcard. Span
// attributes win over resource attributes of the same key.
func attributeMatches(span Span, expr string) bool {
	key, want, hasValue := strings.Cut(expr, "=")
	key = strings.TrimSpace(key)
	value, ok := span.Attributes[key]
	if !ok {
		value, ok = span.Resource[key]
	}
	if !ok || !hasValue {
		return ok
	}
	pattern := strings.ReplaceAll(regexp.QuoteMeta(strings.TrimSpace(want)), `\*`, ".*")
	return regexp.MustCompile("^" + pattern + "$").MatchString(fmt.Sprint(value))
}
