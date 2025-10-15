package query

import (
	"github.com/flanksource/commons-db/query/grammar"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

func parseAndBuildFilteringQuery(query, field string, decodeURL bool) ([]clause.Expression, error) {
	fq, err := ParseFilteringQuery(query, decodeURL)
	if err != nil {
		return nil, err
	}

	var clauses []clause.Expression
	if len(fq.In) > 0 {
		clauses = append(clauses, clause.IN{Column: clause.Column{Raw: true, Name: field}, Values: fq.In})
	}

	if len(fq.Not.In) > 0 {
		clauses = append(clauses, clause.NotConditions{
			Exprs: []clause.Expression{clause.IN{Column: clause.Column{Raw: true, Name: field}, Values: fq.Not.In}},
		})
	}

	for _, g := range fq.Glob {
		clauses = append(clauses, clause.Like{
			Column: clause.Column{Raw: true, Name: field},
			Value:  "%" + g + "%",
		})
	}

	for _, p := range fq.Prefix {
		clauses = append(clauses, clause.Like{
			Column: clause.Column{Raw: true, Name: field},
			Value:  p + "%",
		})
	}

	for _, s := range fq.Suffix {
		clauses = append(clauses, clause.Like{
			Column: clause.Column{Raw: true, Name: field},
			Value:  "%" + s,
		})
	}

	return clauses, nil
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
