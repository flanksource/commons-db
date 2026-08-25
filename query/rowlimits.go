package query

import "fmt"

// The row caps a caller has to keep apart. They answer different questions, so
// none of them substitutes for another:
//
//   - a profile's own limit — the `limit` provider option or a limit-role
//     param — is how many rows the *query* asks the source for. It belongs to
//     the query and travels with it.
//   - PageSize/MaxPageSize bound one *response*: the page a caller gets when it
//     asks for no size, and the largest page it may ask for.
//   - MaxExportRows bounds a whole *export*: the point past which "give me
//     everything" stops, so an unbounded source still terminates.
//   - DefaultSampleLimit bounds a *look at the shape of the data*, which is not
//     a read of it at all: sampling infers columns, so it takes the same few
//     rows however many the query would return.
//
// The first three are what a profile may set for itself; the constants below
// are what applies when it does not.
const (
	// DefaultPageSize is the page a caller gets when it asks for no size.
	DefaultPageSize = 100

	// DefaultMaxPageSize is the largest single page a caller may ask for.
	// Exporting more than this is what MaxExportRows is for.
	DefaultMaxPageSize = 1000

	// DefaultMaxExportRows is where an all-row export stops.
	DefaultMaxExportRows = 100_000

	// DefaultSampleLimit is how many rows column inference reads.
	DefaultSampleLimit = 100
)

// RowLimits are the caps a profile sets for itself. Each is optional: an unset
// cap takes its default, and a set one wins outright — a profile that exports a
// large table raises its own ceiling rather than asking the server to raise it
// for everyone.
type RowLimits struct {
	// PageSize is the page this profile returns when a caller asks for no size.
	PageSize int `json:"pageSize,omitempty" yaml:"pageSize,omitempty"`

	// MaxPageSize is the largest page a caller may ask this profile for.
	MaxPageSize int `json:"maxPageSize,omitempty" yaml:"maxPageSize,omitempty"`

	// MaxExportRows is where an all-row export of this profile stops.
	MaxExportRows int `json:"maxExportRows,omitempty" yaml:"maxExportRows,omitempty"`
}

// Resolve fills each unset cap from its default, so callers work with three
// real numbers rather than deciding what a zero meant.
func (l *RowLimits) Resolve() RowLimits {
	resolved := RowLimits{
		PageSize:      DefaultPageSize,
		MaxPageSize:   DefaultMaxPageSize,
		MaxExportRows: DefaultMaxExportRows,
	}
	if l == nil {
		return resolved
	}
	if l.PageSize != 0 {
		resolved.PageSize = l.PageSize
	}
	if l.MaxPageSize != 0 {
		resolved.MaxPageSize = l.MaxPageSize
	}
	if l.MaxExportRows != 0 {
		resolved.MaxExportRows = l.MaxExportRows
	}
	return resolved
}

// Validate rejects caps that would return nothing or contradict each other. A
// pair is refused rather than narrowed: an author who asked for a default page
// larger than the page a caller may request meant one of the two numbers, and
// picking for them hides the mistake.
func (l *RowLimits) Validate() error {
	if l == nil {
		return nil
	}
	for _, declared := range []struct {
		name  string
		value int
	}{
		{"pageSize", l.PageSize},
		{"maxPageSize", l.MaxPageSize},
		{"maxExportRows", l.MaxExportRows},
	} {
		if declared.value < 0 {
			return fmt.Errorf("limits.%s must be greater than zero, got %d", declared.name, declared.value)
		}
	}
	resolved := l.Resolve()
	if resolved.PageSize > resolved.MaxPageSize {
		return fmt.Errorf(
			"limits.pageSize %d is larger than limits.maxPageSize %d; the default page must be one a caller may ask for",
			resolved.PageSize, resolved.MaxPageSize)
	}
	return nil
}
