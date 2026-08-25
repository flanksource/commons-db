package esdsl

import (
	"fmt"
	"slices"
	"strings"
)

// qualifierOperators lists, per optional Condition field, the operators that
// actually emit it. A qualifier set on any other operator is an authoring
// mistake that would otherwise be silently dropped.
var qualifierOperators = map[string][]Operator{
	"path":               {OpNested},
	"scoreMode":          {OpNested},
	"minimumShouldMatch": {OpBool, OpMultiMatch, OpQueryString, OpSimpleQueryString},
	"slop":               {OpMatchPhrase, OpMatchPhrasePrefix, OpMultiMatch},
	"fuzziness":          {OpMatch, OpMultiMatch, OpFuzzy},
	"multiMatchType":     {OpMultiMatch},
	"matchOperator":      {OpMatch, OpMultiMatch, OpQueryString, OpSimpleQueryString},
	"escape":             {OpQueryString, OpSimpleQueryString},
	"caseInsensitive":    {OpTerm, OpTerms, OpPrefix, OpWildcard, OpRegexp},
	"format":             {OpRange},
	"timeZone":           {OpRange},
	"analyzer":           {OpMatch, OpMatchPhrase, OpMatchPhrasePrefix, OpMultiMatch, OpQueryString, OpSimpleQueryString},
}

// Validate reports the first structural problem in the specification.
func (s Search) Validate() error {
	if s.Query != nil {
		if err := s.Query.Validate("query"); err != nil {
			return err
		}
	}
	for i, sort := range s.Sort {
		if err := sort.validate(fmt.Sprintf("sort[%d]", i)); err != nil {
			return err
		}
	}
	if s.Source != nil {
		if err := validateFieldNames(s.Source.Includes); err != nil {
			return fmt.Errorf("source.includes: %w", err)
		}
		if err := validateFieldNames(s.Source.Excludes); err != nil {
			return fmt.Errorf("source.excludes: %w", err)
		}
	}
	if err := validateFieldNames(s.StoredFields); err != nil {
		return fmt.Errorf("storedFields: %w", err)
	}
	if err := validateFieldNames(s.Fields); err != nil {
		return fmt.Errorf("fields: %w", err)
	}
	if s.Size != nil && *s.Size < 0 {
		return fmt.Errorf("size must not be negative")
	}
	if s.From != nil && *s.From < 0 {
		return fmt.Errorf("from must not be negative")
	}
	if s.TimeField != "" {
		if err := ValidateFieldName(s.TimeField); err != nil {
			return fmt.Errorf("timeField: %w", err)
		}
	}
	if s.TimeFieldFormat != "" {
		if s.TimeField == "" {
			return fmt.Errorf("timeFieldFormat requires timeField")
		}
		if !slices.Contains(TimeFieldFormats(), s.TimeFieldFormat) {
			return fmt.Errorf("unknown timeFieldFormat %q", s.TimeFieldFormat)
		}
	}
	return nil
}

func (s SortBy) validate(path string) error {
	if s.Field == "" {
		return fmt.Errorf("%s: field is required", path)
	}
	if s.Field != "_score" && s.Field != "_doc" {
		if err := ValidateFieldName(s.Field); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	if s.Order != "" && !slices.Contains(sortOrders, strings.ToLower(s.Order)) {
		return fmt.Errorf("%s: order must be %s, got %q", path, strings.Join(sortOrders, " or "), s.Order)
	}
	return nil
}

// Validate reports the first structural problem in the condition tree rooted at
// c. path names the node in error messages.
func (c Condition) Validate(path string) error {
	info, ok := Lookup(c.Op)
	if !ok {
		return fmt.Errorf("%s: unknown operator %q (supported: %s)", path, c.Op, joinOperators())
	}
	if !c.Occur.valid() {
		return fmt.Errorf("%s: unknown occur %q", path, c.Occur)
	}
	if err := c.validateFields(path, info); err != nil {
		return err
	}
	if err := c.validateOperands(path, info); err != nil {
		return err
	}
	if err := c.validateQualifiers(path); err != nil {
		return err
	}
	if err := c.validateQualifierValues(path); err != nil {
		return err
	}
	if !info.Group && len(c.Conditions) > 0 {
		return fmt.Errorf("%s: operator %q does not take child conditions", path, c.Op)
	}
	for i := range c.Conditions {
		if err := c.Conditions[i].Validate(fmt.Sprintf("%s.conditions[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}

func (c Condition) validateFields(path string, info OperatorInfo) error {
	switch {
	case info.NeedsField:
		if len(c.Fields) > 0 {
			return fmt.Errorf("%s: operator %q takes field, not fields", path, c.Op)
		}
		if c.Field == "" {
			return fmt.Errorf("%s: operator %q requires a field", path, c.Op)
		}
	case info.AcceptsFields:
		if c.Field == "" && len(c.Fields) == 0 {
			return fmt.Errorf("%s: operator %q requires field or fields", path, c.Op)
		}
	default:
		if c.Field != "" || len(c.Fields) > 0 {
			return fmt.Errorf("%s: operator %q does not take a field", path, c.Op)
		}
	}
	if c.Field != "" {
		if err := ValidateFieldName(c.Field); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	if err := validateFieldNames(c.Fields); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func (c Condition) validateOperands(path string, info OperatorInfo) error {
	hasRange := c.Gt != nil || c.Gte != nil || c.Lt != nil || c.Lte != nil
	if info.Arity != ArityRange && hasRange {
		return fmt.Errorf("%s: operator %q does not take range bounds", path, c.Op)
	}
	switch info.Arity {
	case AritySingle:
		if c.Value == nil {
			return fmt.Errorf("%s: operator %q requires a value", path, c.Op)
		}
		if len(c.Values) > 0 {
			return fmt.Errorf("%s: operator %q takes value, not values", path, c.Op)
		}
	case ArityMultiple:
		if c.Value == nil && len(c.Values) == 0 {
			return fmt.Errorf("%s: operator %q requires values", path, c.Op)
		}
		if c.Value != nil && len(c.Values) > 0 {
			return fmt.Errorf("%s: operator %q takes value or values, not both", path, c.Op)
		}
	case ArityRange:
		if c.Value != nil || len(c.Values) > 0 {
			return fmt.Errorf("%s: operator %q takes range bounds, not a value", path, c.Op)
		}
		if !hasRange {
			return fmt.Errorf("%s: operator %q requires at least one of gt, gte, lt, lte", path, c.Op)
		}
	case ArityNone, ArityGroup:
		if c.Value != nil || len(c.Values) > 0 {
			return fmt.Errorf("%s: operator %q does not take a value", path, c.Op)
		}
	}
	if c.Op == OpNested {
		if c.Path == "" {
			return fmt.Errorf("%s: nested requires a path", path)
		}
		if err := ValidateFieldName(c.Path); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if len(c.Conditions) == 0 {
			return fmt.Errorf("%s: nested requires at least one condition", path)
		}
	}
	return nil
}

func (c Condition) validateQualifiers(path string) error {
	set := map[string]bool{
		"path":               c.Path != "",
		"scoreMode":          c.ScoreMode != "",
		"minimumShouldMatch": c.MinimumShouldMatch != "",
		"slop":               c.Slop != nil,
		"fuzziness":          c.Fuzziness != "",
		"multiMatchType":     c.MultiMatchType != "",
		"matchOperator":      c.MatchOperator != "",
		"escape":             c.Escape != nil,
		"caseInsensitive":    c.CaseInsensitive != nil,
		"format":             c.Format != "",
		"timeZone":           c.TimeZone != "",
		"analyzer":           c.Analyzer != "",
	}
	for _, name := range sortedQualifiers {
		if !set[name] {
			continue
		}
		allowed := false
		for _, op := range qualifierOperators[name] {
			if op == c.Op {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("%s: %s is not supported by operator %q", path, name, c.Op)
		}
	}
	return nil
}

// validateQualifierValues rejects a qualifier outside its closed vocabulary.
// The backend would reject it too, but only after a round trip and with an
// error that names none of the authoring vocabulary.
func (c Condition) validateQualifierValues(path string) error {
	for _, check := range []struct {
		name    string
		value   string
		allowed []string
	}{
		{"matchOperator", c.MatchOperator, matchOperators},
		{"multiMatchType", c.MultiMatchType, multiMatchTypes},
		{"scoreMode", c.ScoreMode, scoreModes},
	} {
		if check.value == "" || slices.Contains(check.allowed, strings.ToLower(check.value)) {
			continue
		}
		return fmt.Errorf("%s: %s must be one of %s, got %q",
			path, check.name, strings.Join(check.allowed, ", "), check.value)
	}
	return nil
}

// sortedQualifiers keeps qualifier validation errors deterministic.
var sortedQualifiers = []string{
	"analyzer", "caseInsensitive", "escape", "format", "fuzziness", "matchOperator",
	"minimumShouldMatch", "multiMatchType", "path", "scoreMode", "slop", "timeZone",
}

func joinOperators() string {
	names := make([]string, 0, len(catalog))
	for _, info := range catalog {
		names = append(names, string(info.Op))
	}
	return strings.Join(names, ", ")
}
