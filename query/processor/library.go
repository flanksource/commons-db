package processor

import "github.com/flanksource/commons-db/query"

// A JVM logs one exception as many lines: the message, one `\tat …` frame per
// stack entry, a `Caused by:` chain, `Suppressed:` blocks and the `… 23 more`
// elision. A line-oriented shipper stores each of those as its own document, so
// a log search returns forty rows where the operator sees one failure.
//
// javaContinuationCEL recognizes the lines that are not their own event. The
// `+ ""` is what makes it null-safe: a row with no message at all reads as the
// empty string and is simply not a continuation, rather than evaluating to null
// and failing the predicate.
const javaContinuationCEL = `(row.message + "").matches("^\\s*(at\\s|Caused by:|Suppressed:|\\.\\.\\.\\s*[0-9]+\\s+more\\s*$)")`

// javaExceptionCEL lifts the thrown type out of the first line, so the merged
// row can be filtered and grouped by exception class without parsing the
// message again downstream. A line that does not name a throwable yields "".
const javaExceptionCEL = `
	(first.message + "").matches("^[a-zA-Z_$][a-zA-Z0-9_$.]*(Exception|Error|Throwable):")
		? (first.message + "").split(":")[0]
		: ""`

// javaMessageCEL rejoins the batch in the order the JVM printed it. The rows are
// always oldest-first inside a batch, whichever way the query sorted them.
//
// dyn() is required to iterate the batch — see BatchConfig.Set.
const javaMessageCEL = `dyn(batch).map(line, line.message + "").join("\n")`

func init() {
	query.RegisterNamedProcessor(query.NamedProcessor{
		Name:  "java.stacktrace",
		Title: "Java stack trace merge",
		Description: "Folds `at …`, `Caused by:`, `Suppressed:` and `… N more` continuation lines back into the log line that threw, " +
			"so one exception is one row. Adjacent lines sharing a timestamp are merged too; set `boundary` instead of `continuation` " +
			"to turn that off. Assumes newest-first rows — set `order: asc` for a chronological query.",
		Spec: query.ProcessorSpec{
			Type: "cel.batch",
			Config: map[string]any{
				"partition":    []any{"pod", "container"},
				"order":        OrderDescending,
				"continuation": javaContinuationCEL,
				"when":         "count > 1",
				"keep":         KeepFirst,
				"set": map[string]any{
					"message":     javaMessageCEL,
					"exception":   javaExceptionCEL,
					"stack_depth": "count - 1",
				},
			},
		},
	})
}
