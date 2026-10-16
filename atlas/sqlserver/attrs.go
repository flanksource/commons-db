package sqlserver

import "ariga.io/atlas/sql/schema"

type (
	// Identity is an IDENTITY(seed, increment) column.
	Identity struct {
		schema.Attr
		Seed, Increment int64
	}

	// DefaultConstraint names the constraint holding a column default. The
	// inspector sets it from the catalog; the planner names new defaults
	// DF_<table>_<column>.
	DefaultConstraint struct {
		schema.Attr
		Name string
	}

	// Clustered marks a clustered index or primary key. Its absence means
	// nonclustered.
	Clustered struct {
		schema.Attr
	}

	// IndexInclude lists the non-key columns an index INCLUDEs.
	IndexInclude struct {
		schema.Attr
		Columns []string
	}

	// IndexPredicate is a filtered index's WHERE clause.
	IndexPredicate struct {
		schema.Attr
		P string
		// normalized is P's canonical form, set by the differ's Normalize.
		normalized string
	}

	// UniqueConstraint marks a live unique index that backs a UNIQUE
	// constraint, which is dropped with ALTER TABLE ... DROP CONSTRAINT.
	UniqueConstraint struct {
		schema.Attr
	}
)

func attrOf[T schema.Attr](attrs []schema.Attr) (T, bool) {
	for _, attr := range attrs {
		if t, ok := attr.(T); ok {
			return t, true
		}
	}
	var zero T
	return zero, false
}
