package sessions

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"gorm.io/gorm"

	"github.com/flanksource/commons-db/query"
)

// List filters the scalar fields and the window in SQL, and authorizes by
// narrowing to the allowed profiles among those that match. Without a label
// filter SQL also counts, sorts and pages; labels live in the start JSON, so
// with one the matches are paged in Go.
func (s *Store) List(ctx context.Context, filter query.SessionFilter) (query.SessionPage, error) {
	if err := filter.Validate(); err != nil {
		return query.SessionPage{}, err
	}
	empty := query.SessionPage{Items: []query.SessionRecord{}, Shared: true}
	scoped := func() *gorm.DB { return scalarWhere(s.db.WithContext(ctx).Model(&sessionRecord{}), filter) }
	if filter.Allow != nil {
		allowed, err := allowedProfiles(scoped(), filter.Allow)
		if err != nil || len(allowed) == 0 {
			return empty, err
		}
		base := scoped
		scoped = func() *gorm.DB { return base().Where("profile_name IN ?", allowed) }
	}
	if len(filter.Labels) > 0 {
		return s.listInGo(scoped(), filter)
	}
	var total int64
	if err := scoped().Count(&total).Error; err != nil {
		return query.SessionPage{}, fmt.Errorf("count sessions: %w", err)
	}
	page := scoped().Order(sessionOrder(filter.Sort, filter.Desc)).Offset(filter.Offset)
	if filter.Limit > 0 {
		page = page.Limit(filter.Limit)
	}
	records, err := findRecords(page)
	if err != nil {
		return query.SessionPage{}, err
	}
	return query.SessionPage{Items: records, Total: int(total), Shared: true}, nil
}

func (s *Store) listInGo(scoped *gorm.DB, filter query.SessionFilter) (query.SessionPage, error) {
	records, err := findRecords(scoped)
	if err != nil {
		return query.SessionPage{}, err
	}
	filter.Allow = nil
	page, err := query.ApplySessionFilter(records, filter)
	page.Shared = true
	return page, err
}

func findRecords(db *gorm.DB) ([]query.SessionRecord, error) {
	var rows []sessionRecord
	if err := db.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	records := make([]query.SessionRecord, 0, len(rows))
	for _, row := range rows {
		rec, err := row.record()
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, nil
}

func allowedProfiles(db *gorm.DB, allow func(string) bool) ([]string, error) {
	var profiles []string
	if err := db.Distinct("profile_name").Pluck("profile_name", &profiles).Error; err != nil {
		return nil, fmt.Errorf("list session profiles: %w", err)
	}
	allowed := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		if allow(profile) {
			allowed = append(allowed, profile)
		}
	}
	return allowed, nil
}

// sessionOrder is filter.Sort as SQL, with the id breaking ties in the same
// direction. Text sorts by byte order, as the Go filter does.
func sessionOrder(field string, desc bool) string {
	direction, nulls := "ASC", ""
	if desc {
		direction = "DESC"
	}
	column := map[string]string{
		"": "started_at", "startedAt": "started_at", "updatedAt": "updated_at", "eventCount": "event_count",
		"stoppedAt": "stopped_at", "state": `state COLLATE "C"`, "profile": `profile_name COLLATE "C"`,
		"principal": `COALESCE(principal, '') COLLATE "C"`,
	}[field]
	if field == "stoppedAt" {
		// A session that has not stopped sorts before any that has, as in Go.
		nulls = map[bool]string{false: " NULLS FIRST", true: " NULLS LAST"}[desc]
	}
	return fmt.Sprintf(`%s %s%s, id COLLATE "C" %s`, column, direction, nulls, direction)
}

// scalarWhere adds the filter's scalar patterns and startedAt window.
func scalarWhere(db *gorm.DB, filter query.SessionFilter) *gorm.DB {
	for column, patterns := range map[string][]string{
		"id": filter.IDs, "profile_name": filter.Profile, "kind": filter.Kind, "role": filter.Role,
		"state": filter.State, "COALESCE(principal, '')": filter.Principal, "COALESCE(restart_of, '')": filter.RestartOf,
	} {
		if len(patterns) > 0 {
			clause, args := matchItemsSQL(column, patterns)
			db = db.Where(clause, args...)
		}
	}
	if !filter.From.IsZero() {
		db = db.Where("started_at >= ?", filter.From)
	}
	if !filter.To.IsZero() {
		db = db.Where("started_at <= ?", filter.To)
	}
	return db
}

// matchItemsSQL is collections.MatchItems as a SQL predicate over column: any
// exclusion refuses, then any inclusion accepts, and a list of only exclusions
// accepts what none refused. Matching ignores case; `*` is a wildcard only at
// either end.
func matchItemsSQL(column string, patterns []string) (string, []any) {
	var includes, excludes []string
	var includeArgs, excludeArgs []any
	for _, pattern := range normalizeMatchPatterns(patterns) {
		negated := strings.HasPrefix(pattern, "!")
		clause, args := matchPatternSQL(column, strings.TrimPrefix(pattern, "!"))
		if negated {
			excludes, excludeArgs = append(excludes, "NOT ("+clause+")"), append(excludeArgs, args...)
		} else {
			includes, includeArgs = append(includes, clause), append(includeArgs, args...)
		}
	}
	switch {
	case len(includes) == 0 && len(excludes) == 0:
		return "FALSE", nil
	case len(includes) == 0:
		return strings.Join(excludes, " AND "), excludeArgs
	case len(excludes) == 0:
		return "(" + strings.Join(includes, " OR ") + ")", includeArgs
	}
	return "(" + strings.Join(includes, " OR ") + ") AND " + strings.Join(excludes, " AND "),
		append(includeArgs, excludeArgs...)
}

// matchPatternSQL mirrors collections' matchPattern for one pattern.
func matchPatternSQL(column, pattern string) (string, []any) {
	if pattern == "*" {
		return "TRUE", nil
	}
	clauses := []string{"LOWER(" + column + ") = LOWER(?)"}
	args := []any{pattern}
	prefixed, suffixed := strings.HasPrefix(pattern, "*"), strings.HasSuffix(pattern, "*")
	if prefixed && suffixed {
		clauses, args = append(clauses, column+" ILIKE ?"), append(args, "%"+likeEscape(strings.TrimPrefix(strings.TrimSuffix(pattern, "*"), "*"))+"%")
	}
	if prefixed {
		clauses, args = append(clauses, column+" ILIKE ?"), append(args, "%"+likeEscape(strings.TrimPrefix(pattern, "*")))
	}
	if suffixed {
		clauses, args = append(clauses, column+" ILIKE ?"), append(args, likeEscape(strings.TrimSuffix(pattern, "*"))+"%")
	}
	return strings.Join(clauses, " OR "), args
}

func likeEscape(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

// normalizeMatchPatterns splits comma lists and unescapes each part exactly as
// collections.MatchItems does before matching.
func normalizeMatchPatterns(patterns []string) []string {
	var normalized []string
	for _, pattern := range patterns {
		parts := strings.Split(pattern, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" && len(parts) > 1 {
				continue
			}
			decoded, err := url.QueryUnescape(part)
			if err != nil {
				continue
			}
			normalized = append(normalized, decoded)
		}
	}
	return normalized
}
