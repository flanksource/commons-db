package query

// Row is a single result record keyed by column name. It is a type alias for
// the generic map so provider code (ported from duty/dataquery) and CEL
// evaluation can treat rows uniformly.
type Row = map[string]any

// Result is the output of executing a Profile: the tabular rows plus any named
// context objects (Policy/Plan/Integrations side panels, each
// produced by a SubQuery).
type Result struct {
	// Profile is the name of the Profile that produced this Result.
	Profile string `json:"profile,omitempty" yaml:"profile,omitempty"`

	// Rows are the primary tabular records.
	Rows []Row `json:"rows" yaml:"rows"`

	// Context holds named side objects keyed by SubQuery name.
	Context map[string]any `json:"context,omitempty" yaml:"context,omitempty"`
}
