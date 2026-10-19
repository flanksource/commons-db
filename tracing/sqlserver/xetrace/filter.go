package xetrace

import (
	"strings"
	"time"

	"github.com/flanksource/commons-db/query/sqltext"
	"github.com/flanksource/commons/collections"
)

// dmlGroup is the virtual token added to an event's type tokens when its
// StatementType is a write (INSERT/UPDATE/DELETE/MERGE), so a `type=DML` filter
// matches any of them without the caller enumerating each verb.
const dmlGroup = "DML"

// dmlTypes is the set of StatementTypes covered by the DML group alias.
var dmlTypes = map[sqltext.StatementType]struct{}{
	sqltext.StmtInsert: {},
	sqltext.StmtUpdate: {},
	sqltext.StmtDelete: {},
	sqltext.StmtMerge:  {},
}

// FilterableTypes is every token an EventFilter.Types entry may name: each
// StatementType plus the virtual DML group. It is the option set a UI offers,
// derived from the same constants typeTokens emits so a picker can never drift
// from what the filter actually matches.
var FilterableTypes = []string{
	string(sqltext.StmtSelect),
	string(sqltext.StmtInsert),
	string(sqltext.StmtUpdate),
	string(sqltext.StmtDelete),
	string(sqltext.StmtMerge),
	string(sqltext.StmtExec),
	string(sqltext.StmtDDL),
	string(sqltext.StmtOther),
	dmlGroup,
}

// typeTokens are the filter tokens for an event's StatementType: the concrete
// verb plus the DML group alias for writes, so collections.MatchAny can resolve
// a `DML` pattern against an UPDATE event.
func typeTokens(t sqltext.StatementType) []string {
	if _, ok := dmlTypes[t]; ok {
		return []string{string(t), dmlGroup}
	}
	return []string{string(t)}
}

// splitPatterns flattens comma-joined flag values (`--type=SELECT,UPDATE`) and
// trims blanks, so repeated flags and comma lists are equivalent.
func splitPatterns(values []string) []string {
	var out []string
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// matchTokens reports whether any of the event's structured tokens satisfies the
// patterns. With no patterns every event passes. When the event has no tokens
// (e.g. a table-less SET/error row), an exclusion-only pattern set still passes
// it (deny-list intent) while a positive pattern set drops it.
func matchTokens(tokens, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	if len(tokens) == 0 {
		return collections.IsExclusionOnlyPatterns(patterns)
	}
	matched, _ := collections.MatchAny(tokens, patterns...)
	return matched
}

func stringToken(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}

// EventFilter narrows captured events by statement type and referenced table,
// using collections.MatchItems semantics (case-insensitive, `*` wildcards, `!`
// exclusion). Both lists are matched against the event's STRUCTURED tokens
// (typeTokens / Tables), never the raw SQL — so no LIKE/regex is involved.
type EventFilter struct {
	Events      []string
	Databases   []string
	Users       []string
	Apps        []string
	Hosts       []string
	Types       []string
	Tables      []string
	MinDuration time.Duration
}

// match reports whether e passes the filter. The zero filter passes everything.
func (f EventFilter) match(e Event) bool {
	if e.Name == EventXMLDeadlockReport {
		return matchTokens([]string{e.Name}, splitPatterns(f.Events)) &&
			matchTokens(e.DeadlockDatabases, splitPatterns(f.Databases))
	}
	return matchTokens([]string{e.Name}, splitPatterns(f.Events)) &&
		matchTokens(stringToken(e.DatabaseName), splitPatterns(f.Databases)) &&
		matchTokens(stringToken(e.Username), splitPatterns(f.Users)) &&
		matchTokens(stringToken(e.ClientApp), splitPatterns(f.Apps)) &&
		matchTokens(stringToken(e.ClientHost), splitPatterns(f.Hosts)) &&
		matchTokens(typeTokens(e.StatementType), splitPatterns(f.Types)) &&
		matchTokens(e.Tables, splitPatterns(f.Tables)) &&
		(f.MinDuration <= 0 || e.Duration <= 0 || e.Duration >= f.MinDuration)
}
