package recordstore

import (
	"fmt"
	"reflect"
	"slices"
	"sync"

	"github.com/flanksource/commons-db/query"
)

// Schemas is the catalog of kinds and their columns. A backend that stores
// rows by column (sqlite) resolves a kind through it, and a typed result
// registry fills it, so the two agree without either one owning the other.
type Schemas struct {
	mu    sync.RWMutex
	kinds map[string][]query.ColumnDef
}

// NewSchemas returns an empty catalog.
func NewSchemas() *Schemas { return &Schemas{kinds: map[string][]query.ColumnDef{}} }

// Register declares kind's columns. A kind is declared once: a second
// declaration with different columns would leave rows already written under
// the first shape unreadable, so it is an error rather than a replacement.
func (s *Schemas) Register(kind string, columns []query.ColumnDef) error {
	if err := ValidateKind(kind); err != nil {
		return err
	}
	if len(columns) == 0 {
		return fmt.Errorf("kind %q declares no columns", kind)
	}
	for _, column := range columns {
		if err := column.Validate(); err != nil {
			return fmt.Errorf("kind %q: %w", kind, err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.kinds[kind]; ok {
		if reflect.DeepEqual(existing, columns) {
			return nil
		}
		return fmt.Errorf("kind %q is already declared with different columns", kind)
	}
	s.kinds[kind] = slices.Clone(columns)
	return nil
}

// Columns resolves kind, and refuses a kind nothing declared.
func (s *Schemas) Columns(kind string) ([]query.ColumnDef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	columns, ok := s.kinds[kind]
	if !ok {
		return nil, fmt.Errorf("kind %q has no declared schema", kind)
	}
	return slices.Clone(columns), nil
}
