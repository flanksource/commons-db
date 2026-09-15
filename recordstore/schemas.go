package recordstore

import (
	"fmt"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/flanksource/commons-db/query"
)

// Retention says how long a stream keeps its rows.
type Retention int

const (
	// RetainStream keeps a stream whole until it expires, the backend ttl after
	// its first append unless Expire moves it.
	RetainStream Retention = iota

	// RetainRows keeps each row the backend ttl after its own append: every
	// append slides the stream's expiry to the ttl from now and trims the rows
	// appended longer than the ttl ago. It is for a stream that accumulates
	// for as long as something writes to it.
	RetainRows
)

func (r Retention) String() string {
	switch r {
	case RetainStream:
		return "stream"
	case RetainRows:
		return "rows"
	default:
		return fmt.Sprintf("retention(%d)", int(r))
	}
}

// KindOptions say how a kind's streams store its rows.
type KindOptions struct {
	// Key names a string column whose value identifies a row within a stream.
	// A keyed stream holds each key once: an append skips a row whose key is
	// already stored. Empty leaves the kind unkeyed, where every row appended
	// is stored.
	Key string

	// Retention says how long a stream keeps its rows.
	Retention Retention
}

// KindSchema is everything a kind declares: its columns and how its streams
// store them.
type KindSchema struct {
	Kind    string
	Columns []query.ColumnDef
	Options KindOptions
}

// SchemaResolver resolves a kind to its schema, and refuses a kind nothing
// declared. Schemas.Kind is one.
type SchemaResolver func(kind string) (KindSchema, error)

// Schemas is the catalog of kinds and their columns. A backend resolves a kind
// through it — to store rows by column (sqlite), to find a kind's key and
// retention (every backend) — and a typed result registry fills it, so the two
// agree without either one owning the other.
type Schemas struct {
	mu    sync.RWMutex
	kinds map[string]KindSchema
}

// NewSchemas returns an empty catalog.
func NewSchemas() *Schemas { return &Schemas{kinds: map[string]KindSchema{}} }

// Register declares kind's columns and options. A kind is declared once: a
// second declaration with different columns or options would leave rows
// already written under the first unreadable or wrongly deduplicated, so it is
// an error rather than a replacement.
func (s *Schemas) Register(kind string, columns []query.ColumnDef, options KindOptions) error {
	schema := KindSchema{Kind: kind, Columns: slices.Clone(columns), Options: options}
	if err := schema.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.kinds[kind]; ok {
		if reflect.DeepEqual(existing, schema) {
			return nil
		}
		return fmt.Errorf("kind %q is already declared with different columns or options", kind)
	}
	s.kinds[kind] = schema
	return nil
}

// Kind resolves kind, and refuses a kind nothing declared.
func (s *Schemas) Kind(kind string) (KindSchema, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	schema, ok := s.kinds[kind]
	if !ok {
		return KindSchema{}, fmt.Errorf("kind %q has no declared schema", kind)
	}
	schema.Columns = slices.Clone(schema.Columns)
	return schema, nil
}

// Validate refuses a schema no backend could store as declared.
func (k KindSchema) Validate() error {
	if err := ValidateKind(k.Kind); err != nil {
		return err
	}
	if len(k.Columns) == 0 {
		return fmt.Errorf("kind %q declares no columns", k.Kind)
	}
	for _, column := range k.Columns {
		if err := column.Validate(); err != nil {
			return fmt.Errorf("kind %q: %w", k.Kind, err)
		}
	}
	if k.Options.Retention != RetainStream && k.Options.Retention != RetainRows {
		return fmt.Errorf("kind %q declares unknown %s", k.Kind, k.Options.Retention)
	}
	if k.Options.Key == "" {
		return nil
	}
	index := slices.IndexFunc(k.Columns, func(column query.ColumnDef) bool { return column.Name == k.Options.Key })
	if index < 0 {
		return fmt.Errorf("kind %q key %q is not one of its columns", k.Kind, k.Options.Key)
	}
	if k.Columns[index].Type != query.ColumnTypeString {
		return fmt.Errorf("kind %q key %q is a %s column, not a string", k.Kind, k.Options.Key, k.Columns[index].Type)
	}
	return nil
}

// ResolveKind resolves kind through resolver and checks the schema it returns
// describes kind, so a backend never stores rows under a schema meant for
// another kind.
func ResolveKind(resolver SchemaResolver, kind string) (KindSchema, error) {
	schema, err := resolver(kind)
	if err != nil {
		return KindSchema{}, err
	}
	if schema.Kind != kind {
		return KindSchema{}, fmt.Errorf("kind %q resolved to the schema of kind %q", kind, schema.Kind)
	}
	if err := schema.Validate(); err != nil {
		return KindSchema{}, err
	}
	return schema, nil
}

// RetentionTTL is the ttl a stream of schema's kind keeps each row for, or
// zero for a kind that keeps its stream whole. A kind retaining rows needs a
// backend with a ttl: without one, nothing would ever leave the stream.
func (k KindSchema) RetentionTTL(ttl time.Duration) (time.Duration, error) {
	if k.Options.Retention != RetainRows {
		return 0, nil
	}
	if ttl <= 0 {
		return 0, fmt.Errorf("kind %q retains rows for the backend ttl, but the backend has none", k.Kind)
	}
	return ttl, nil
}

// RowKeys reads the key of every row of an append to a stream of schema's kind,
// in row order, or nil for an unkeyed kind. A row without a non-empty string
// key, or two rows with the same key, refuse the whole append.
func (k KindSchema) RowKeys(rows []Row) ([]string, error) {
	if k.Options.Key == "" {
		return nil, nil
	}
	keys := make([]string, len(rows))
	seen := make(map[string]int, len(rows))
	for index, row := range rows {
		key, ok := row[k.Options.Key].(string)
		if !ok || key == "" {
			return nil, fmt.Errorf("kind %q row %d: key %q must be a non-empty string, got %#v", k.Kind, index, k.Options.Key, row[k.Options.Key])
		}
		if first, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("kind %q rows %d and %d both have key %q = %q; an append names each key once", k.Kind, first, index, k.Options.Key, key)
		}
		seen[key] = index
		keys[index] = key
	}
	return keys, nil
}

// Unstored keeps the rows of an append whose key stored does not report as
// already in the stream, with their keys, and counts the rows it skipped. With
// no keys (an unkeyed kind) every row is kept.
func Unstored(rows []Row, keys []string, stored func(key string) bool) (kept []Row, keptKeys []string, skipped int64) {
	if keys == nil {
		return rows, nil, 0
	}
	kept, keptKeys = make([]Row, 0, len(rows)), make([]string, 0, len(rows))
	for index, row := range rows {
		if stored(keys[index]) {
			skipped++
			continue
		}
		kept, keptKeys = append(kept, row), append(keptKeys, keys[index])
	}
	return kept, keptKeys, skipped
}
