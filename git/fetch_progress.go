package git

import (
	"bytes"
	"context"
	"regexp"
	"strconv"
	"strings"
)

// FetchProgress is one update of a fetch, as git's own progress meter shows
// it ("Receiving objects:  45% (450/1000), 12.00 MiB | 3.00 MiB/s").
// Repository (redacted) and Ref identify the fetch; the last update of every
// fetch has Done set, with Err when it failed.
type FetchProgress struct {
	Repository string
	Ref        string
	// Phase is git's phase label: "Enumerating objects", "Counting objects",
	// "Compressing objects" (Remote, on the server), "Receiving objects",
	// "Resolving deltas".
	Phase       string
	Remote      bool
	Percent     int
	Current     int64
	Total       int64 // 0 when git reports no bound
	Transferred string
	Rate        string
	PhaseDone   bool
	Done        bool
	Err         error
}

// FetchObserver receives every FetchProgress of the fetches made under a
// context. Fetches of different repositories may report concurrently.
type FetchObserver func(FetchProgress)

type fetchObserverKey struct{}

// WithFetchObserver makes every fetch run under ctx (clones, cache checkouts,
// reads that fetch a missing commit) report its progress to observer.
func WithFetchObserver(ctx context.Context, observer FetchObserver) context.Context {
	return context.WithValue(ctx, fetchObserverKey{}, observer)
}

func fetchObserver(ctx context.Context) FetchObserver {
	observer, _ := ctx.Value(fetchObserverKey{}).(FetchObserver)
	return observer
}

var progressLine = regexp.MustCompile(`^(remote: )?([A-Z][A-Za-z ]*[a-z]):\s+(?:(\d+)%\s+\((\d+)/(\d+)\)|(\d+))(.*)$`)

// parseFetchProgress reads one line of `git fetch --progress` stderr; lines
// that are not a progress meter (summaries, ref updates, errors) report false.
func parseFetchProgress(line string) (FetchProgress, bool) {
	match := progressLine.FindStringSubmatch(strings.TrimSpace(line))
	if match == nil {
		return FetchProgress{}, false
	}
	progress := FetchProgress{Phase: match[2], Remote: match[1] != ""}
	if match[3] != "" {
		progress.Percent, _ = strconv.Atoi(match[3])
		progress.Current, _ = strconv.ParseInt(match[4], 10, 64)
		progress.Total, _ = strconv.ParseInt(match[5], 10, 64)
	} else {
		progress.Current, _ = strconv.ParseInt(match[6], 10, 64)
	}
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(match[7]), ","))
	if trimmed, done := strings.CutSuffix(rest, "done."); done {
		progress.PhaseDone = true
		rest = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(trimmed), ","))
	}
	if transferred, rate, ok := strings.Cut(rest, "|"); ok {
		progress.Transferred, progress.Rate = strings.TrimSpace(transferred), strings.TrimSpace(rate)
	}
	return progress, true
}

var transferredUnits = map[string]float64{"bytes": 1.0 / 1024, "KiB": 1, "MiB": 1024, "GiB": 1024 * 1024}

// transferredKiB reads git's throughput size ("104.33 MiB") as whole KiB;
// false when transferred is empty or not in a unit git prints.
func transferredKiB(transferred string) (int64, bool) {
	value, unit, ok := strings.Cut(strings.TrimSpace(transferred), " ")
	scale, known := transferredUnits[unit]
	number, err := strconv.ParseFloat(value, 64)
	if !ok || !known || err != nil {
		return 0, false
	}
	return int64(number * scale), true
}

// progressWriter splits git's stderr on the carriage returns its meter redraws
// with, reports each progress line and keeps every other line in other, so an
// error still carries git's message without the meter.
type progressWriter struct {
	onProgress func(FetchProgress)
	pending    bytes.Buffer
	other      bytes.Buffer
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.pending.Write(p)
	for {
		buffered := w.pending.Bytes()
		end := bytes.IndexAny(buffered, "\r\n")
		if end < 0 {
			return len(p), nil
		}
		w.line(string(buffered[:end]), buffered[end] == '\n')
		w.pending.Next(end + 1)
	}
}

// flush handles a trailing line git ended without a newline.
func (w *progressWriter) flush() {
	if w.pending.Len() > 0 {
		w.line(w.pending.String(), true)
		w.pending.Reset()
	}
}

func (w *progressWriter) line(line string, newline bool) {
	if progress, ok := parseFetchProgress(line); ok {
		w.onProgress(progress)
		return
	}
	if strings.TrimSpace(line) == "" {
		return
	}
	w.other.WriteString(line)
	if newline {
		w.other.WriteByte('\n')
	}
}
