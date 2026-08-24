package devtools

// Store is the server's memory of what recently ran: a bounded history of
// execution summaries, the expensive detail behind the most recent of them, and
// a tail of the lines the process logged.
//
// Nothing here is persisted. A record holds request and response bodies, which
// is exactly what must not outlive the process that was asked to capture it —
// unlike session events, which are durable on purpose (see sessions/store.go).

import (
	"fmt"
	"sync"
	"time"

	"github.com/flanksource/commons-db/query"
)

const (
	// DefaultMaxRecords is how many execution summaries the history keeps. It is
	// a session's worth of queries, not a day's.
	DefaultMaxRecords = 200

	// DefaultMaxLogLines bounds the process-wide tail.
	DefaultMaxLogLines = 2000

	// DefaultMaxDetailBytes bounds the bodies and previews held across all
	// records. Detail is evicted oldest-first when this is exceeded; the summary
	// it belonged to survives, so the history stays complete and only the
	// expensive half thins out.
	DefaultMaxDetailBytes = 32 << 20

	// DefaultDetailTTL is how long detail is worth keeping. It matches the
	// session cap: past it, nobody is still debugging that request.
	DefaultDetailTTL = 15 * time.Minute
)

// ErrDetailEvicted distinguishes "that detail aged out" from "no such record".
// Conflating them would tell a user their id was wrong when it was not.
type ErrDetailEvicted struct {
	ID     string
	Reason string
}

func (e *ErrDetailEvicted) Error() string {
	return fmt.Sprintf("devtools detail for %s is no longer held: %s", e.ID, e.Reason)
}

// Options configures NewStore.
type Options struct {
	MaxRecords     int
	MaxLogLines    int
	MaxDetailBytes int64
	DetailTTL      time.Duration
}

type detailEntry struct {
	id       string
	recorded time.Time
	bytes    int64
	detail   query.ExecutionDetail
}

type Store struct {
	records *Ring[query.ExecutionSummary]
	logs    *Ring[query.LogLine]

	maxDetailBytes int64
	detailTTL      time.Duration

	mu          sync.Mutex
	details     map[string]*detailEntry
	detailOrder []string
	detailBytes int64
	evicted     map[string]string
}

func NewStore(options Options) *Store {
	if options.MaxRecords <= 0 {
		options.MaxRecords = DefaultMaxRecords
	}
	if options.MaxLogLines <= 0 {
		options.MaxLogLines = DefaultMaxLogLines
	}
	if options.MaxDetailBytes <= 0 {
		options.MaxDetailBytes = DefaultMaxDetailBytes
	}
	if options.DetailTTL <= 0 {
		options.DetailTTL = DefaultDetailTTL
	}
	return &Store{
		records: NewRing(RingOptions[query.ExecutionSummary]{
			Max:   options.MaxRecords,
			Stamp: func(item *query.ExecutionSummary, sequence int64) { item.Sequence = sequence },
		}),
		logs: NewRing(RingOptions[query.LogLine]{
			Max:   options.MaxLogLines,
			Stamp: func(item *query.LogLine, sequence int64) { item.Sequence = sequence },
		}),
		maxDetailBytes: options.MaxDetailBytes,
		detailTTL:      options.DetailTTL,
		details:        map[string]*detailEntry{},
		evicted:        map[string]string{},
	}
}

// Add files a finished recorder: the summary goes to every open console, the
// detail is held for whoever opens that row.
func (s *Store) Add(recorder *query.Recorder) query.ExecutionSummary {
	if recorder == nil {
		return query.ExecutionSummary{}
	}
	detail := recorder.Detail()

	s.mu.Lock()
	entry := &detailEntry{
		id: detail.Summary.ID, recorded: time.Now(),
		bytes: estimateDetailBytes(detail), detail: detail,
	}
	s.details[entry.id] = entry
	s.detailOrder = append(s.detailOrder, entry.id)
	s.detailBytes += entry.bytes
	s.pruneDetailLocked()
	s.mu.Unlock()

	// The ring stamps the sequence, so the summary the console receives and the
	// summary inside the detail must be the same object's value — otherwise a row
	// fetched by id reports sequence 0 and cannot be resumed past.
	sequence := s.records.Append(detail.Summary)
	s.mu.Lock()
	if held, ok := s.details[entry.id]; ok {
		held.detail.Summary.Sequence = sequence
	}
	s.mu.Unlock()

	detail.Summary.Sequence = sequence
	return detail.Summary
}

// Records returns the summaries past a sequence the caller already holds.
func (s *Store) Records(after int64) []query.ExecutionSummary {
	return s.records.ItemsAfter(after)
}

// Detail returns one record's expensive half. It reports eviction as an error
// rather than as an empty record, because "we no longer hold it" and "it made no
// requests" are different answers.
func (s *Store) Detail(id string) (query.ExecutionDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneDetailLocked()

	if entry, ok := s.details[id]; ok {
		return entry.detail, nil
	}
	if reason, ok := s.evicted[id]; ok {
		return query.ExecutionDetail{}, &ErrDetailEvicted{ID: id, Reason: reason}
	}
	return query.ExecutionDetail{}, fmt.Errorf("no devtools record %q", id)
}

// Log appends one line to the process-wide tail.
func (s *Store) Log(line query.LogLine) { s.logs.Append(line) }

// Logs returns tail lines past a sequence the caller already holds.
func (s *Store) Logs(after int64) []query.LogLine { return s.logs.ItemsAfter(after) }

// SubscribeRecords and SubscribeLogs are the two streams a console follows.
func (s *Store) SubscribeRecords(after int64) ([]query.ExecutionSummary, <-chan query.ExecutionSummary, func()) {
	return s.records.SubscribeFrom(after)
}

func (s *Store) SubscribeLogs(after int64) ([]query.LogLine, <-chan query.LogLine, func()) {
	return s.logs.SubscribeFrom(after)
}

// Stats reports what the store holds and what it has let go, so a client that
// finds a gap in its sequences can tell a quiet server from a full buffer.
type Stats struct {
	Records         int   `json:"records"`
	RecordsDropped  int64 `json:"recordsDropped"`
	OldestSequence  int64 `json:"oldestSequence"`
	LogLines        int   `json:"logLines"`
	LogsDropped     int64 `json:"logsDropped"`
	LogsOldest      int64 `json:"logsOldestSequence"`
	DetailsHeld     int   `json:"detailsHeld"`
	DetailBytes     int64 `json:"detailBytes"`
	DetailsEvicted  int   `json:"detailsEvicted"`
	MaxDetailBytes  int64 `json:"maxDetailBytes"`
	DetailTTLSecond int64 `json:"detailTtlSeconds"`
}

func (s *Store) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{
		Records: len(s.records.Items()), RecordsDropped: s.records.Dropped(),
		OldestSequence: s.records.OldestSequence(),
		LogLines:       len(s.logs.Items()), LogsDropped: s.logs.Dropped(),
		LogsOldest:      s.logs.OldestSequence(),
		DetailsHeld:     len(s.details),
		DetailBytes:     s.detailBytes,
		DetailsEvicted:  len(s.evicted),
		MaxDetailBytes:  s.maxDetailBytes,
		DetailTTLSecond: int64(s.detailTTL / time.Second),
	}
}

// Clear discards the history. Sequences keep climbing so a console resuming
// from a Last-Event-ID is not replayed ids it has already applied.
func (s *Store) Clear() {
	s.records.Clear()
	s.logs.Clear()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.details = map[string]*detailEntry{}
	s.detailOrder = nil
	s.detailBytes = 0
	s.evicted = map[string]string{}
}

func (s *Store) pruneDetailLocked() {
	deadline := time.Now().Add(-s.detailTTL)
	for len(s.detailOrder) > 0 {
		oldest := s.detailOrder[0]
		entry, ok := s.details[oldest]
		if !ok {
			s.detailOrder = s.detailOrder[1:]
			continue
		}
		expired := entry.recorded.Before(deadline)
		if !expired && s.detailBytes <= s.maxDetailBytes {
			return
		}
		reason := fmt.Sprintf("detail budget exceeded (%d bytes held)", s.detailBytes)
		if expired {
			reason = fmt.Sprintf("older than %s", s.detailTTL)
		}
		delete(s.details, oldest)
		s.detailOrder = s.detailOrder[1:]
		s.detailBytes -= entry.bytes
		s.evicted[oldest] = reason
	}
}

// estimateDetailBytes sizes a record by the only fields that are ever large.
// It is an estimate on purpose: marshalling every record to measure it would
// pay the serialization cost for records nobody opens.
func estimateDetailBytes(detail query.ExecutionDetail) int64 {
	var total int64
	for _, diagnostics := range detail.Operations {
		total += int64(len(diagnostics.Response.Preview) + len(diagnostics.Request.Query))
	}
	if detail.HAR != nil {
		for _, entry := range detail.HAR.Log.Entries {
			total += entry.Response.Content.Size
			if entry.Request.PostData != nil {
				total += int64(len(entry.Request.PostData.Text))
			}
		}
	}
	for _, line := range detail.Logs {
		total += int64(len(line.Message))
	}
	return total
}
