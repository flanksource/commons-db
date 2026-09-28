// Package deadlocks turns SQL Server xml_deadlock_report graphs into decoded,
// index-resolved and classified Deadlocks.
//
// A deadlock victim is gone by the time anyone looks for it: the only record of
// the collision is the graph SQL Server reported. This package is the one place
// the derived facts come from: which index a page lock sat on, which recurring
// shape the cycle has, and its process-independent signature.
package deadlocks

import "time"

// Deadlock is one xml_deadlock_report: the list row the CLI and the web table
// show. Processes and Resources ride along whole so a JSON consumer and the
// row detail have the complete graph without a second read.
type Deadlock struct {
	// ID is <UTC timestamp>-<first victim process id>, e.g.
	// 20260910T164750043Z-processe80b448c8. The event timestamp alone is not
	// unique: two cycles broken in the same millisecond share it.
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`

	// Database is the victim's current database, which is the database the
	// collision is attributed to. Participants can sit in other databases; the
	// database filter matches any of them.
	Database string `json:"database"`

	Shape     Shape  `json:"shape"`
	Signature string `json:"signature"`

	Objects   []string `json:"objects"`
	Indexes   []string `json:"indexes"`
	LockTypes []string `json:"lockTypes"`

	Victims      int `json:"victims"`
	Participants int `json:"participants"`

	VictimHost      string `json:"victimHost"`
	VictimApp       string `json:"victimApp"`
	VictimStatement string `json:"victimStatement"`

	Processes []Process  `json:"processes"`
	Resources []Resource `json:"resources"`
}

// Graph is a Deadlock plus the report XML it was decoded from — the detail
// view, where the raw graph can be copied out or opened in SSMS.
type Graph struct {
	Deadlock
	XML string `json:"xml"`
}

// Process is one participant of the cycle.
type Process struct {
	ID     string `json:"id"`
	Victim bool   `json:"victim"`

	SPID   int    `json:"spid"`
	ECID   int    `json:"ecid"`
	Status string `json:"status,omitempty"`

	Host      string `json:"host,omitempty"`
	HostPID   int    `json:"hostPid,omitempty"`
	ClientApp string `json:"clientApp,omitempty"`
	Login     string `json:"login,omitempty"`
	Database  string `json:"database,omitempty"`

	IsolationLevel  string `json:"isolationLevel,omitempty"`
	TranCount       int    `json:"tranCount"`
	TransactionName string `json:"transactionName,omitempty"`
	// LastTranStarted is the server's local time, not UTC, as the graph prints
	// it with no zone. It is kept as text rather than guessed into UTC: compare
	// WaitingSince, which is.
	LastTranStarted string `json:"lastTranStarted,omitempty"`

	// LockMode is the mode the process was waiting to be granted.
	LockMode     string `json:"lockMode,omitempty"`
	WaitResource string `json:"waitResource,omitempty"`
	WaitTimeMs   int64  `json:"waitTimeMs"`
	// WaitingSince is when the wait began, in UTC: the report timestamp less
	// WaitTimeMs. It is unset for a process that was not waiting.
	WaitingSince *time.Time `json:"waitingSince,omitempty"`
	LogUsed      int64      `json:"logUsed"`
	Priority     int        `json:"priority"`

	Frames []Frame `json:"frames,omitempty"`
	// InputBuffer is the batch the client sent, as far as SQL Server captured
	// it: every long batch captured so far was cut at 1,023 characters.
	InputBuffer string `json:"inputBuffer,omitempty"`

	// Statement is the statement the top frame was running, sliced out of an
	// ad hoc batch by the frame's offsets; for a batch without offsets, or a
	// procedure frame whose offsets index the module, it is the whole buffer.
	// When InputBufferTruncated, it is the part the buffer kept.
	Statement string `json:"statement"`
	// StatementLength is the statement's full length in characters (UTF-16
	// code units), which exceeds len(Statement) when the buffer was truncated.
	StatementLength int `json:"statementLength"`
	// InputBufferTruncated says the running statement extends past the input
	// buffer SQL Server captured, so Statement is only a fragment of it.
	InputBufferTruncated bool `json:"inputBufferTruncated"`
}

// Frame is one executionStack frame: the statement inside a batch or
// procedure the process was running.
type Frame struct {
	ProcName string `json:"procName,omitempty"`
	Line     int    `json:"line,omitempty"`
	// StmtStart and StmtEnd are the statement's UTF-16 byte offsets into the
	// batch or module text, a prepared statement's parameter declaration
	// included. StmtEnd is inclusive of the last character, and -1 means the
	// end of the text; SQL Server omits either at its default (0, -1).
	StmtStart *int   `json:"stmtStart,omitempty"`
	StmtEnd   *int   `json:"stmtEnd,omitempty"`
	SQLHandle string `json:"sqlHandle,omitempty"`
	Text      string `json:"text,omitempty"`
}

// Resource is one lock resource in the cycle. The report repeats a resource
// once per owner/waiter pair it contributes to the cycle; Decode merges those
// repeats by lock id, so each resource appears once with every request on it.
type Resource struct {
	// Kind is the report's element name: keylock, pagelock, ridlock,
	// objectlock, exchangeEvent, …
	Kind   string `json:"kind"`
	LockID string `json:"lockId,omitempty"`
	// Mode is the resource's granted group mode.
	Mode string `json:"mode,omitempty"`

	DatabaseID int `json:"databaseId,omitempty"`
	// ObjectName is the object as the report names it, usually
	// database.schema.table.
	ObjectName string `json:"objectName,omitempty"`
	// Object is the table the lock sits on, without database or schema.
	Object    string    `json:"object,omitempty"`
	Index     string    `json:"index,omitempty"`
	IndexType IndexType `json:"indexType,omitempty"`
	HobtID    int64     `json:"hobtId,omitempty"`
	FileID    int       `json:"fileId,omitempty"`
	PageID    int64     `json:"pageId,omitempty"`

	// Resolution says where Object and Index came from — see Resolution.
	Resolution Resolution `json:"resolution"`

	Owners  []LockRequest `json:"owners"`
	Waiters []LockRequest `json:"waiters"`

	// Attrs keeps every attribute the element carried, so a resource kind
	// this package does not interpret is still shown whole.
	Attrs map[string]string `json:"attrs,omitempty"`
}

// LockRequest is one process's request on a resource. An owner whose
// RequestType is "wait" was not granted anything: it is queued ahead of the
// waiter, which is how the report expresses a lock queue inside the cycle.
type LockRequest struct {
	ProcessID   string `json:"processId"`
	Mode        string `json:"mode"`
	RequestType string `json:"requestType,omitempty"`
}

// Granted reports whether the request holds the lock rather than queueing for
// it.
func (r LockRequest) Granted() bool { return r.RequestType != "wait" }

// IndexType is sys.indexes.type_desc for the index a lock sits on.
type IndexType string

const (
	IndexClustered    IndexType = "CLUSTERED"
	IndexNonclustered IndexType = "NONCLUSTERED"
	IndexHeap         IndexType = "HEAP"
)

// Resolution records how a resource's object and index were named.
type Resolution string

const (
	// ResolutionNamed: the report itself named the index (key locks do).
	ResolutionNamed Resolution = "named"
	// ResolutionUnresolved: the report carried only a hobt id (page and row
	// locks do), and Resolve has not looked it up yet.
	ResolutionUnresolved Resolution = "unresolved"
	// ResolutionResolved: the report carried only a hobt id, found in the
	// connected database's current catalog.
	ResolutionResolved Resolution = "resolved"
	// ResolutionRebuiltSince: the hobt id no longer exists. Rebuilding an index
	// replaces it, so the graph predates a rebuild.
	ResolutionRebuiltSince Resolution = "rebuilt-since"
	// ResolutionOtherDatabase: the lock is in a database other than the
	// connected one, whose catalog this read does not consult.
	ResolutionOtherDatabase Resolution = "other-database"
	// ResolutionNotApplicable: the resource is not an index lock (an exchange
	// event, an application lock, …).
	ResolutionNotApplicable Resolution = "not-applicable"
)
