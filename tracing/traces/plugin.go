// Package traces runs trace plugins: kinds of capture that each declare a params
// form and a record schema, and emit records a record store keeps.
package traces

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/flanksource/clicky/rpc"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
)

// TraceHandler is a trace kind's implementation over its params P and its
// records R.
type TraceHandler[P, R any] interface {
	// Params are the defaults a session starts from; P's type is the params
	// form a client renders.
	Params() P
	// Schema declares how R's records are stored and read: its title, time
	// and key columns, indexes. R's own columns are reflected from its fields.
	Schema() recordresults.ResultType[R]
	// Handle captures until ctx is done, or until its source is exhausted,
	// emitting each record. It may read back the records it already emitted.
	Handle(ctx dbcontext.Context, params P, records Emitter[R], store Records[R]) error
}

// Preparer is a TraceHandler whose capture must be set up before its session
// runs: a subscription that must not miss a record, or a server-side session
// whose failure should refuse the start. Prepare runs as the session starts,
// before it reports running; Handle then runs under the context Prepare
// returns, which can carry what it set up, and release runs once Handle has
// returned, or at once if the capture never started.
type Preparer[P, R any] interface {
	Prepare(ctx dbcontext.Context, params P, records Emitter[R]) (prepared dbcontext.Context, release func(), err error)
}

// Capabilities say how a kind captures: Live captures what happens while a
// session runs, Historical reads what already happened and ends by itself,
// Follow goes on to capture what happens next.
type Capabilities struct {
	Live       bool `json:"live,omitempty"`
	Historical bool `json:"historical,omitempty"`
	Follow     bool `json:"follow,omitempty"`
}

// Emitter takes a handler's records.
type Emitter[R any] interface {
	// Emit waits while the session's buffer is full, and returns ctx's error
	// once it is done.
	Emit(ctx context.Context, record R) error
	// TryEmit never waits: a record a full buffer cannot take is dropped and
	// counted, so a handler called on someone else's goroutine never stalls it.
	TryEmit(record R) bool
}

// Records reads back the records a session has committed.
type Records[R any] interface {
	Scan(ctx context.Context, afterSeq int64, fn func(seq int64, record R) error) error
}

// Validator is implemented by params that check themselves once decoded.
type Validator interface {
	Validate() error
}

// TracePlugin is a trace kind as the kinds catalog holds it.
type TracePlugin interface {
	// Title is the kind's human name.
	Title() string
	// Params is the JSON schema of the kind's params, with their defaults: the
	// form a client renders to start a capture.
	Params() (*rpc.OpenAPISchema, error)
	// Schema is the record-store schema the kind's records are stored under
	// when it is registered as kind.
	Schema(kind string) (recordstore.KindSchema, error)
	Capabilities() Capabilities
	// ValidateParams decodes params as a session start would, refusing what
	// the session would refuse.
	ValidateParams(params json.RawMessage) error

	register(registry *recordresults.Registry, kind string) error
	open(ctx dbcontext.Context, options openOptions) (*source, error)
}

// openOptions are what a session gives the capture it opens.
type openOptions struct {
	kind, stream string
	params       json.RawMessage
	store        scanner
	bufferRows   int
}

// Handler is a TraceHandler as a TracePlugin, with the processing its records
// get before they are stored.
type Handler[P, R any] struct {
	handler      TraceHandler[P, R]
	capabilities Capabilities

	dedup      *dedupOption[P, R]
	truncateAt int
	parseJSON  bool
	mask       bool
	keep       []string
}

type dedupOption[P, R any] struct {
	key    func(R) string
	window func(P) time.Duration
}

// NewHandler makes handler a plugin with capabilities.
func NewHandler[P, R any](handler TraceHandler[P, R], capabilities Capabilities) *Handler[P, R] {
	return &Handler[P, R]{handler: handler, capabilities: capabilities}
}

// WithDeduplication drops a record whose key a session already emitted within
// the window its params give; a window of zero or less keeps every record.
func (h *Handler[P, R]) WithDeduplication(key func(R) string, window func(P) time.Duration) *Handler[P, R] {
	h.dedup = &dedupOption[P, R]{key: key, window: window}
	return h
}

// WithSecretMasking masks secrets in every record (maskSecrets), never masking
// the keys named in keep, at any depth.
func (h *Handler[P, R]) WithSecretMasking(keep ...string) *Handler[P, R] {
	h.mask, h.keep = true, keep
	return h
}

// WithTruncation cuts every string in a record longer than maxBytes, listing
// what it cut in the record's Truncation, which R must embed.
func (h *Handler[P, R]) WithTruncation(maxBytes int) *Handler[P, R] {
	h.truncateAt = maxBytes
	return h
}

// WithJSONProcessor parses every string in a record that holds a JSON object or
// array, so its fields are stored, masked and read as structure.
func (h *Handler[P, R]) WithJSONProcessor() *Handler[P, R] {
	h.parseJSON = true
	return h
}

// processors are the record processing the handler's options ask for, in the
// order they run: truncate first, so nothing parses an unbounded string, then
// parse, so masking reaches into what was embedded JSON, then mask last, so
// nothing after it adds a secret back. Only the text and JSON columns of
// schema are truncated.
func (h *Handler[P, R]) processors(schema recordstore.KindSchema) []rowProcessor {
	var processors []rowProcessor
	if h.truncateAt > 0 {
		var fixed []string
		for _, column := range schema.Columns {
			if column.Type != query.ColumnTypeString && column.Type != query.ColumnTypeJSON {
				fixed = append(fixed, column.Name)
			}
		}
		processors = append(processors, truncateStrings(h.truncateAt, fixed))
	}
	if h.parseJSON {
		processors = append(processors, parseEmbeddedJSON)
	}
	if h.mask {
		processors = append(processors, maskSecrets(h.keep))
	}
	return processors
}

func (h *Handler[P, R]) Title() string { return h.handler.Schema().Title }

func (h *Handler[P, R]) Capabilities() Capabilities { return h.capabilities }

func (h *Handler[P, R]) Params() (*rpc.OpenAPISchema, error) {
	defaults := h.handler.Params()
	schema := rpc.SchemaForStruct(defaults)
	var values map[string]any
	if err := roundTrip(defaults, &values); err != nil {
		return nil, fmt.Errorf("params defaults: %w", err)
	}
	for name, value := range values {
		if property, ok := schema.Properties[name]; ok && value != nil && !isZero(value) {
			property.Default = value
		}
	}
	return schema, nil
}

func (h *Handler[P, R]) Schema(kind string) (recordstore.KindSchema, error) {
	schema, err := h.resultType(kind).KindSchema()
	if err != nil {
		return schema, err
	}
	if h.truncateAt > 0 && !slices.ContainsFunc(schema.Columns, func(column query.ColumnDef) bool { return column.Name == TruncatedColumn }) {
		return schema, fmt.Errorf("records of kind %q are truncated, so they must embed traces.Truncation", kind)
	}
	return schema, nil
}

func (h *Handler[P, R]) ValidateParams(params json.RawMessage) error {
	_, err := h.decodeParams(params)
	return err
}

func (h *Handler[P, R]) register(registry *recordresults.Registry, kind string) error {
	return recordresults.RegisterResultType(registry, h.resultType(kind))
}

// open decodes params and prepares the handler's capture into a new source,
// which the session starts once its stream exists. A Preparer's setup runs
// here, so a capture that cannot be set up refuses its session's start.
func (h *Handler[P, R]) open(ctx dbcontext.Context, options openOptions) (*source, error) {
	params, err := h.decodeParams(options.params)
	if err != nil {
		return nil, err
	}
	schema, err := h.Schema(options.kind)
	if err != nil {
		return nil, err
	}
	src := newSource(options.stream, options.bufferRows, schema.Options.Key, schema.Options.OnConflict == recordstore.OnConflictReplace)
	emitter := &emitter[R]{source: src, process: pipeline(h.processors(schema))}
	if h.dedup != nil {
		if window := h.dedup.window(params); window > 0 {
			emitter.dedup = newDeduplicator(window, h.dedup.key)
		}
	}
	captureCtx, release := ctx, func() {}
	if preparer, ok := h.handler.(Preparer[P, R]); ok {
		prepared, prepareRelease, err := preparer.Prepare(ctx, params, emitter)
		if err != nil {
			return nil, err
		}
		captureCtx = prepared
		if prepareRelease != nil {
			release = prepareRelease
		}
	}
	records := storedRecords[R]{store: options.store, stream: options.stream}
	src.prepare(captureCtx, func(capture dbcontext.Context) error {
		return h.handler.Handle(capture, params, emitter, records)
	}, release)
	return src, nil
}

func (h *Handler[P, R]) resultType(kind string) recordresults.ResultType[R] {
	resultType := h.handler.Schema()
	resultType.Kind = kind
	return resultType
}

// decodeParams decodes params over the handler's defaults, refusing unknown
// fields and trailing data, then lets params that validate themselves do so.
// No params, or null, start from the defaults.
func (h *Handler[P, R]) decodeParams(params json.RawMessage) (P, error) {
	decoded := h.handler.Params()
	trimmed := bytes.TrimSpace(params)
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&decoded); err != nil {
			return decoded, fmt.Errorf("params: %w", err)
		}
		if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
			return decoded, errors.New("params: trailing data after the params object")
		}
	}
	if validator, ok := any(decoded).(Validator); ok {
		if err := validator.Validate(); err != nil {
			return decoded, fmt.Errorf("params: %w", err)
		}
	} else if validator, ok := any(&decoded).(Validator); ok {
		if err := validator.Validate(); err != nil {
			return decoded, fmt.Errorf("params: %w", err)
		}
	}
	return decoded, nil
}

func roundTrip(value, into any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, into)
}

// isZero reports a JSON value that says nothing a form should prefill.
func isZero(value any) bool {
	switch v := value.(type) {
	case string:
		return v == ""
	case float64:
		return v == 0
	case bool:
		return !v
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	return false
}
