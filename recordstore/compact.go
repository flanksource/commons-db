// Compaction: rules that drop rows from the middle of a kind's streams, and
// merging several streams into one.
package recordstore

import (
	"context"
	"fmt"
	"time"
)

// CompactRule selects rows a store may drop when it compacts: those Where
// selects, a CEL expression over the row bound as `row`, and appended longer
// than OlderThan ago by the kind's time column. A rule sets either or both.
// A stream's last row is never dropped: it is what keeps its high seq.
type CompactRule struct {
	Where     string
	OlderThan time.Duration
}

// MergeOptions say what a merge copies. Where, a CEL expression over the row
// bound as `row`, keeps only the rows it selects; DeleteSources removes the
// sources in the same write as the merged stream is created.
type MergeOptions struct {
	Where         string
	DeleteSources bool
}

// Merger is a backend that merges streams: it creates target, which must not
// exist, holding the rows of sources — all of one kind — ordered by the
// kind's time column, then by the order of sources, then by seq. A key held
// by several sources keeps its first row, or, for a kind that replaces
// stored rows, its last. The rows are appended now, so a kind retaining rows
// keeps them from the merge rather than from their first append.
type Merger interface {
	Merge(ctx context.Context, target string, sources []string, options MergeOptions) (AppendResult, error)
}

// SkipsSeqs reports whether a stream of the kind may skip seqs: one whose
// rows are replaced or compacted away leaves their seqs behind.
func (k KindSchema) SkipsSeqs() bool {
	return k.Options.OnConflict == OnConflictReplace || len(k.Options.Compact) > 0
}

// RefuseSkippedSeqs is the error a backend whose seqs cannot skip refuses
// schema's kind with, or nil for a kind whose seqs never skip.
func (k KindSchema) RefuseSkippedSeqs() error {
	switch {
	case k.Options.OnConflict == OnConflictReplace:
		return fmt.Errorf("kind %q replaces stored rows: %w", k.Kind, ErrUnsupported)
	case len(k.Options.Compact) > 0:
		return fmt.Errorf("kind %q compacts its rows: %w", k.Kind, ErrUnsupported)
	default:
		return nil
	}
}

// validateCompaction refuses a rule that selects nothing, or ages rows the
// kind has no time column to age them by.
func (k KindSchema) validateCompaction() error {
	for index, rule := range k.Options.Compact {
		switch {
		case rule.Where == "" && rule.OlderThan == 0:
			return fmt.Errorf("kind %q compact rule %d selects no rows; set Where, OlderThan or both", k.Kind, index)
		case rule.OlderThan < 0:
			return fmt.Errorf("kind %q compact rule %d has a negative age %s", k.Kind, index, rule.OlderThan)
		case rule.OlderThan > 0 && k.Options.TimeColumn == "":
			return fmt.Errorf("kind %q compact rule %d needs a time column to age rows by", k.Kind, index)
		}
	}
	return nil
}
