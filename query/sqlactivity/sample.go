// Package sqlactivity samples the sessions and requests a Postgres or SQL
// Server is running, top style: every read returns one row per active request.
package sqlactivity

import (
	"strings"
	"time"
	"unicode"
)

const (
	EnginePostgres  = "postgres"
	EngineSQLServer = "sqlserver"
)

// Sample is one session's request as a read saw it. Every column filters and
// sorts. Fields an engine does not expose stay zero, which the engine column
// makes plain.
type Sample struct {
	SampledAt            time.Time `json:"sampledAt" pretty:"label=Sampled" filter:"time" sort:"sampledAt"`
	Engine               string    `json:"engine" filter:"terms" sort:"engine"`
	Database             string    `json:"database" filter:"terms" sort:"database"`
	SessionID            int64     `json:"sessionId" pretty:"label=Session" filter:"exact" sort:"sessionId"`
	RequestID            int64     `json:"requestId" pretty:"label=Request" filter:"exact" sort:"requestId"`
	BlockedBy            int64     `json:"blockedBy" pretty:"label=Blocked by" filter:"exact" sort:"blockedBy"`
	Blocked              bool      `json:"blocked" filter:"boolean" sort:"blocked"`
	User                 string    `json:"user" filter:"terms" sort:"user"`
	Application          string    `json:"application" filter:"terms" sort:"application"`
	ClientHost           string    `json:"clientHost" pretty:"label=Client host" filter:"terms" sort:"clientHost"`
	State                string    `json:"state" filter:"terms" sort:"state"`
	Command              string    `json:"command" filter:"terms" sort:"command"`
	WaitType             string    `json:"waitType" pretty:"label=Wait" filter:"terms" sort:"waitType"`
	WaitResource         string    `json:"waitResource" pretty:"label=Wait resource" filter:"exact" sort:"waitResource"`
	WaitMs               float64   `json:"waitMs" pretty:"label=Waited,type=duration,unit=ms" filter:"duration" sort:"waitMs"`
	StartedAt            time.Time `json:"startedAt" pretty:"label=Started" filter:"time" sort:"startedAt"`
	TransactionStartedAt time.Time `json:"transactionStartedAt" pretty:"label=Transaction started" filter:"time" sort:"transactionStartedAt"`
	ElapsedMs            float64   `json:"elapsedMs" pretty:"label=Elapsed,type=duration,unit=ms" filter:"duration" sort:"elapsedMs"`
	TransactionMs        float64   `json:"transactionMs" pretty:"label=Transaction,type=duration,unit=ms" filter:"duration" sort:"transactionMs"`
	CPUMs                float64   `json:"cpuMs" pretty:"label=CPU,type=duration,unit=ms" filter:"duration" sort:"cpuMs"`
	LogicalReads         int64     `json:"logicalReads" filter:"range" sort:"logicalReads"`
	Reads                int64     `json:"reads" filter:"range" sort:"reads"`
	Writes               int64     `json:"writes" filter:"range" sort:"writes"`
	MemoryBytes          int64     `json:"memoryBytes" pretty:"label=Memory,type=bytes" filter:"range" sort:"memoryBytes"`
	OpenTransactions     int64     `json:"openTransactions" filter:"range" sort:"openTransactions"`
	QueryID              string    `json:"queryId" pretty:"label=Query id" filter:"exact" sort:"queryId"`
	SQL                  string    `json:"sql" pretty:"label=SQL" filter:"text" sort:"sql"`
}

// derive fills what follows from the server time and the row's own times:
// elapsed (when the engine does not report it), transaction age and blocked.
func (s *Sample) derive(at time.Time) {
	s.SampledAt = at
	if s.ElapsedMs == 0 && !s.StartedAt.IsZero() {
		s.ElapsedMs = milliseconds(at.Sub(s.StartedAt))
	}
	if !s.TransactionStartedAt.IsZero() {
		s.TransactionMs = milliseconds(at.Sub(s.TransactionStartedAt))
	}
	s.Blocked = s.BlockedBy != 0
}

// commandOf is the first keyword of statement, upper cased, for an engine
// that reports no command of its own.
func commandOf(statement string) string {
	statement = strings.TrimSpace(statement)
	if end := strings.IndexFunc(statement, func(r rune) bool { return !unicode.IsLetter(r) }); end >= 0 {
		statement = statement[:end]
	}
	return strings.ToUpper(statement)
}

func milliseconds(d time.Duration) float64 {
	if d < 0 {
		return 0
	}
	return float64(d) / float64(time.Millisecond)
}
