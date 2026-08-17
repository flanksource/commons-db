package processor

import (
	"github.com/flanksource/commons-db/logs"
	"github.com/flanksource/commons-db/query"
)

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

const javaStackTraceMax = 500

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

// dedupeLastSeenCEL is the timestamp of the newest row that collapsed into the
// group, and dedupeFirstSeenCEL the other end of the same span.
//
// A group keeps arrival order and a log query arrives in timestamp order, so
// the two extremes are always the two ends of the batch — but which end is
// which depends on the direction. Reading both and comparing them is what makes
// the preset correct under either: a Loki profile returns newest-first, and a
// Kubernetes one is ascending because that is the only direction its API can
// page in. Pinning this to a position silently swapped the two labels for
// whichever profile disagreed.
const dedupeLastSeenCEL = `dyn(batch)[0].timestamp > dyn(batch)[count - 1].timestamp ? dyn(batch)[0].timestamp : dyn(batch)[count - 1].timestamp`

const dedupeFirstSeenCEL = `dyn(batch)[0].timestamp > dyn(batch)[count - 1].timestamp ? dyn(batch)[count - 1].timestamp : dyn(batch)[0].timestamp`

func init() {
	query.RegisterNamedProcessor(query.NamedProcessor{
		Name:        "logs.json",
		Title:       "Parse JSON logs",
		Description: "Extracts message, severity, source and host from a JSON log body, promotes the remaining fields as columns, and hashes the parsed message for later deduplication. Non-JSON lines pass through unchanged, so this can precede a multiline processor.",
		Spec: query.ProcessorSpec{
			Type:   "logs.parse",
			Config: map[string]any{"format": logs.FormatJSON},
		},
	})

	query.RegisterNamedProcessor(query.NamedProcessor{
		Name:        "logs.logfmt",
		Title:       "Parse logfmt logs",
		Description: "Extracts message and severity from a logfmt body, promotes the remaining key/value pairs as columns, and hashes the parsed message for later deduplication. Non-logfmt lines pass through unchanged.",
		Spec: query.ProcessorSpec{
			Type:   "logs.parse",
			Config: map[string]any{"format": logs.FormatLogfmt},
		},
	})

	query.RegisterNamedProcessor(query.NamedProcessor{
		Name:  "logs.dedupe",
		Title: "Collapse repeated log lines",
		Description: "Folds lines whose message is the same once variable parts (ids, durations, addresses) are tokenized out into a single row carrying " +
			"`count` and the first/last time it was seen. Keys on `hash`, so `Timeout after 31ms` and `Timeout after 5006ms` count as one error. " +
			"Works in either sort direction — set `partition` yourself for a different key. Paged, it folds each page and remembers the groups it has " +
			"already emitted, so a line surfaces once per walk carrying the count from the page it appeared on.",
		Spec: query.ProcessorSpec{
			Type: "cel.dedupe",
			Config: map[string]any{
				"partition": []any{"hash"},
				"keep":      KeepFirst,
				"set": map[string]any{
					"count":     "count",
					"lastSeen":  dedupeLastSeenCEL,
					"firstSeen": dedupeFirstSeenCEL,
				},
			},
		},
	})

	query.RegisterNamedProcessor(query.NamedProcessor{
		Name:  "java.stacktrace",
		Title: "Java stack trace merge",
		Description: "Folds `at …`, `Caused by:`, `Suppressed:` and `… N more` continuation lines back into the log line that threw, " +
			"so one exception is one row. Adjacent lines sharing a timestamp are merged too; set `boundary` instead of `continuation` " +
			"to turn that off. Traces longer than 500 lines are emitted in bounded chunks. Assumes newest-first rows — set `order: asc` " +
			"for a chronological query.",
		Spec: query.ProcessorSpec{
			Type: "cel.batch",
			Config: map[string]any{
				"partition":    []any{"pod", "container"},
				"order":        OrderDescending,
				"max":          javaStackTraceMax,
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
