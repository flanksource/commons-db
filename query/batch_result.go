package query

import "github.com/flanksource/commons-db/context"

// BatchEvent is what a SQL program reports about one batch while it runs: a
// message the server sent (Message set, as it arrives) or the batch ending
// (Done set, with the rows it changed where the driver reports them).
type BatchEvent struct {
	// Batch is the batch's 1-based Index; Run its repetition under `GO <count>`.
	Batch     int `json:"batch"`
	Run       int `json:"run"`
	StartLine int `json:"startLine"`
	// Message is a PRINT or low-severity RAISERROR text, where the driver
	// surfaces them as they arrive (SQL Server).
	Message      string `json:"message,omitempty"`
	Done         bool   `json:"done,omitempty"`
	RowsAffected *int64 `json:"rowsAffected,omitempty"`
}

type batchObserverKey struct{}

// WithBatchObserver returns a context whose SQL programs report each batch's
// messages and end to observe while they run, on the goroutine running the
// program. The finished Result.Batches is unchanged by it.
func WithBatchObserver(ctx context.Context, observe func(BatchEvent)) context.Context {
	return ctx.WithValue(batchObserverKey{}, observe)
}

// BatchObserver returns the observer WithBatchObserver attached, or nil when
// nobody is watching.
func BatchObserver(ctx context.Context) func(BatchEvent) {
	observe, _ := ctx.Value(batchObserverKey{}).(func(BatchEvent))
	return observe
}

// BatchResult is what one batch of a SQL program returned. A program is a
// query the provider runs as written rather than wrapping it for paging: a
// statement that is not a single SELECT (EXEC, INSERT, UPDATE, DELETE), several
// statements, or a script of GO-separated batches.
//
// A batch repeated by `GO <count>` reports once per run, under the same Index
// and lines, numbered by Run.
type BatchResult struct {
	// Index is the batch's 1-based position in the program.
	Index int `json:"index"`
	// Run is the 1-based repetition of the batch; always 1 unless `GO <count>`.
	Run int `json:"run"`
	// StartLine and EndLine are the 1-based lines of the program the batch spans.
	StartLine int `json:"startLine"`
	EndLine   int `json:"endLine"`
	// ResultSets are the batch's result sets in the order the server returned
	// them. Only result sets with columns are kept.
	ResultSets [][]Row `json:"resultSets,omitempty"`
	// RowsAffected is the rows the batch changed, nil where the driver does not
	// report it for a statement that returns rows.
	RowsAffected *int64 `json:"rowsAffected,omitempty"`
	// Messages are the informational messages the server sent (PRINT,
	// RAISERROR below severity 11), where the driver surfaces them.
	Messages []string `json:"messages,omitempty"`
}

// LastResultSet is the last result set with columns across batches, which is
// what a program's Result.Rows holds.
func LastResultSet(batches []BatchResult) []Row {
	for index := len(batches) - 1; index >= 0; index-- {
		if sets := batches[index].ResultSets; len(sets) > 0 {
			return sets[len(sets)-1]
		}
	}
	return nil
}
