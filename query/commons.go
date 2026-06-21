package query

import (
	"github.com/flanksource/commons-db/query/grammar"
	"gorm.io/gorm"
)

// ParseFilteringQuery parses a filtering query string
func ParseFilteringQuery(query string, decodeURL bool) (grammar.FilteringQuery, error) {
	if query == "" {
		return grammar.FilteringQuery{}, nil
	}

	q, err := grammar.ParseFilteringQueryV2(query, decodeURL)
	if err != nil {
		return grammar.FilteringQuery{}, err
	}

	return q, nil
}

// OrQueries combines multiple gorm queries with OR logic
func OrQueries(db *gorm.DB, queries ...*gorm.DB) *gorm.DB {
	if len(queries) == 0 {
		return db
	}

	if len(queries) == 1 {
		return db.Where(queries[0])
	}

	union := queries[0]
	for i, q := range queries {
		if i == 0 {
			continue
		}

		union = union.Or(q)
	}

	return db.Where(union)
}
