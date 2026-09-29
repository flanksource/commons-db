// Batches: several stream writes a producer hands the store at once, applied
// in one transaction with each entry isolated and the outcome recorded by id.
package recordstore

import (
	"context"
	"errors"
	"time"
)

// ErrSchemaConflict reports rows whose kind is stored with another key, conflict
// policy or column storage than the rows' own schema declares: storing them
// would make the rows already written mean something else.
var ErrSchemaConflict = errors.New("record kind schema conflict")

// BatchOp is what one batch entry does to its stream.
type BatchOp string

const (
	BatchAppend BatchOp = "append"
	BatchSeal   BatchOp = "seal"
	BatchExpire BatchOp = "expire"
	BatchTrim   BatchOp = "trim"
	BatchDelete BatchOp = "delete"
	BatchReopen BatchOp = "reopen"
)

// Producer identifies the process that made a batch: Instance is unique per
// process lifetime, and Seq counts that instance's batches from 1.
type Producer struct {
	Instance string `json:"instance"`
	Seq      int64  `json:"seq"`
	PID      int    `json:"pid,omitempty"`
	Host     string `json:"host,omitempty"`
	Build    string `json:"build,omitempty"`
}

// BatchEntry is one write of a batch, doing what the backend method of the
// same name does.
type BatchEntry struct {
	Op     BatchOp `json:"op"`
	Stream string  `json:"stream"`

	// Kind and Rows are an append's. Seal seals the stream after the append,
	// in the same entry.
	Kind string `json:"kind,omitempty"`
	Rows []Row  `json:"-"`
	Seal bool   `json:"seal,omitempty"`

	// Generation, when set, fences the entry to that incarnation of the
	// stream: any other one, or none, fails the entry as not found. A reopen
	// requires it.
	Generation string `json:"generation,omitempty"`

	// TTL is an expire's, and Before a trim's.
	TTL    time.Duration `json:"ttl,omitempty"`
	Before time.Time     `json:"before,omitzero"`
}

// Batch is the entries a producer writes at once. Schemas, when set, are the
// kinds as the producer declares them, which the store reconciles additively
// with the kinds it holds; a kind without one is resolved as for any append.
type Batch struct {
	ID       string       `json:"id"`
	Producer Producer     `json:"producer"`
	Schemas  []KindSchema `json:"-"`
	Entries  []BatchEntry `json:"entries"`
}

// BatchResult is what a batch did, entry by entry, in entry order.
type BatchResult struct {
	ID      string        `json:"id"`
	Entries []EntryResult `json:"entries"`
}

// EntryResult is what one entry did: an append's result, a trim's metadata,
// or the error that rolled the entry back.
type EntryResult struct {
	Append *AppendResult `json:"append,omitempty"`
	Meta   *Meta         `json:"meta,omitempty"`
	Error  *BatchError   `json:"error,omitempty"`
}

// BatchErrorCode classifies an entry's error so it survives being recorded
// and read back by another process.
type BatchErrorCode string

const (
	BatchErrorSealed      BatchErrorCode = "sealed"
	BatchErrorNotFound    BatchErrorCode = "not_found"
	BatchErrorCapacity    BatchErrorCode = "capacity"
	BatchErrorConflict    BatchErrorCode = "conflict"
	BatchErrorMismatch    BatchErrorCode = "mismatch"
	BatchErrorUnsupported BatchErrorCode = "unsupported"
	BatchErrorInvalid     BatchErrorCode = "invalid"
)

// batchErrorSentinels is the sentinel each code unwraps to. An invalid entry
// has none.
var batchErrorSentinels = map[BatchErrorCode]error{
	BatchErrorSealed:      ErrSealed,
	BatchErrorNotFound:    ErrNotFound,
	BatchErrorCapacity:    ErrCapacity,
	BatchErrorConflict:    ErrSchemaConflict,
	BatchErrorMismatch:    ErrSchemaMismatch,
	BatchErrorUnsupported: ErrUnsupported,
}

// BatchError is an entry's error as its batch recorded it.
type BatchError struct {
	Code    BatchErrorCode `json:"code"`
	Message string         `json:"message"`
}

// NewBatchError codes err by the first sentinel it wraps, in the order the
// codes are declared; an error wrapping none means the entry itself was
// invalid.
func NewBatchError(err error) *BatchError {
	for _, code := range []BatchErrorCode{BatchErrorSealed, BatchErrorNotFound, BatchErrorCapacity, BatchErrorConflict, BatchErrorMismatch, BatchErrorUnsupported} {
		if errors.Is(err, batchErrorSentinels[code]) {
			return &BatchError{Code: code, Message: err.Error()}
		}
	}
	return &BatchError{Code: BatchErrorInvalid, Message: err.Error()}
}

func (e *BatchError) Error() string { return e.Message }

func (e *BatchError) Unwrap() error { return batchErrorSentinels[e.Code] }

// Submitter hands a batch to the process that writes a store and returns
// what the batch did there, for a process that only reads the store. It sets
// the batch's producer.
type Submitter func(ctx context.Context, batch Batch) (BatchResult, error)

// BatchAppender is a backend that applies batches. AppendBatch applies a batch
// id once: a repeat returns the outcome recorded the first time, so a producer
// may hand the same batch over again after a crash. An entry that fails rolls
// back alone and reports its error in its result; an error AppendBatch returns
// means nothing was applied. BatchOutcome reads a recorded outcome, reporting
// false for a batch id never applied.
type BatchAppender interface {
	AppendBatch(ctx context.Context, batch Batch) (BatchResult, error)
	BatchOutcome(ctx context.Context, id string) (BatchResult, bool, error)
}
