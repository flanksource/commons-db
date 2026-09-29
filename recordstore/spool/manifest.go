// The manifest: a published batch's description, with explicit JSON names and
// string enums so a build reading another build's batch reads it the same.
package spool

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

// ManifestFormat is the manifest format this build writes and reads.
const ManifestFormat = 1

// ErrManifestFormat reports a batch published in a manifest format this build
// does not read.
var ErrManifestFormat = errors.New("spool manifest format unsupported")

const manifestFile = "manifest.json"

type manifest struct {
	Format   int                  `json:"format"`
	ID       string               `json:"id"`
	Producer recordstore.Producer `json:"producer"`
	Created  time.Time            `json:"created"`
	Schemas  []manifestSchema     `json:"schemas,omitempty"`
	Entries  []manifestEntry      `json:"entries"`
}

type manifestSchema struct {
	Kind              string           `json:"kind"`
	Columns           []manifestColumn `json:"columns"`
	Key               string           `json:"key,omitempty"`
	Retention         string           `json:"retention"`
	OnConflict        string           `json:"onConflict"`
	TimeColumn        string           `json:"timeColumn,omitempty"`
	Indexes           [][]string       `json:"indexes,omitempty"`
	Dynamic           bool             `json:"dynamic,omitempty"`
	MaxDynamicColumns int              `json:"maxDynamicColumns,omitempty"`
}

type manifestColumn struct {
	Name string           `json:"name"`
	Type query.ColumnType `json:"type"`
}

// manifestEntry is a batch entry. An append's rows are in File, in Format,
// Rows of them, with the file's SHA256; an append of no rows has no file.
type manifestEntry struct {
	Op         recordstore.BatchOp `json:"op"`
	Stream     string              `json:"stream"`
	Kind       string              `json:"kind,omitempty"`
	File       string              `json:"file,omitempty"`
	Format     string              `json:"format,omitempty"`
	Rows       int                 `json:"rows,omitempty"`
	SHA256     string              `json:"sha256,omitempty"`
	Seal       bool                `json:"seal,omitempty"`
	Generation string              `json:"generation,omitempty"`
	TTL        string              `json:"ttl,omitempty"`
	Before     *time.Time          `json:"before,omitempty"`
}

// namePattern is what a batch id and a producer instance may hold: they name
// directories.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func validIdentity(id, instance string) error {
	if !namePattern.MatchString(id) {
		return fmt.Errorf("spool: batch id %q must match %s", id, namePattern)
	}
	if !namePattern.MatchString(instance) {
		return fmt.Errorf("spool: batch %q producer instance %q must match %s", id, instance, namePattern)
	}
	return nil
}

func schemaOf(schema recordstore.KindSchema) manifestSchema {
	encoded := manifestSchema{
		Kind: schema.Kind, Key: schema.Options.Key,
		Retention: schema.Options.Retention.String(), OnConflict: schema.Options.OnConflict.String(),
		TimeColumn: schema.Options.TimeColumn, Dynamic: schema.Options.Dynamic, MaxDynamicColumns: schema.Options.MaxDynamicColumns,
	}
	for _, index := range schema.Options.Indexes {
		encoded.Indexes = append(encoded.Indexes, index.Columns)
	}
	for _, column := range schema.Columns {
		encoded.Columns = append(encoded.Columns, manifestColumn{Name: column.Name, Type: column.Type})
	}
	return encoded
}

func (s manifestSchema) schema() (recordstore.KindSchema, error) {
	schema := recordstore.KindSchema{Kind: s.Kind, Options: recordstore.KindOptions{
		Key: s.Key, TimeColumn: s.TimeColumn, Dynamic: s.Dynamic, MaxDynamicColumns: s.MaxDynamicColumns,
	}}
	for _, columns := range s.Indexes {
		schema.Options.Indexes = append(schema.Options.Indexes, recordstore.IndexDef{Columns: columns})
	}
	switch s.Retention {
	case recordstore.RetainStream.String():
		schema.Options.Retention = recordstore.RetainStream
	case recordstore.RetainRows.String():
		schema.Options.Retention = recordstore.RetainRows
	default:
		return recordstore.KindSchema{}, fmt.Errorf("kind %q has unknown retention %q", s.Kind, s.Retention)
	}
	switch s.OnConflict {
	case recordstore.OnConflictSkip.String():
		schema.Options.OnConflict = recordstore.OnConflictSkip
	case recordstore.OnConflictReplace.String():
		schema.Options.OnConflict = recordstore.OnConflictReplace
	default:
		return recordstore.KindSchema{}, fmt.Errorf("kind %q has unknown conflict policy %q", s.Kind, s.OnConflict)
	}
	for _, column := range s.Columns {
		schema.Columns = append(schema.Columns, query.ColumnDef{Name: column.Name, Type: column.Type})
	}
	return schema, nil
}

// entryOf is entry as the manifest records it, but for its rows' file.
func entryOf(entry recordstore.BatchEntry) manifestEntry {
	encoded := manifestEntry{
		Op: entry.Op, Stream: entry.Stream, Kind: entry.Kind, Seal: entry.Seal, Generation: entry.Generation,
	}
	if entry.TTL != 0 {
		encoded.TTL = entry.TTL.String()
	}
	if !entry.Before.IsZero() {
		before := entry.Before
		encoded.Before = &before
	}
	return encoded
}

// entry is the batch entry the manifest records, without its rows.
func (e manifestEntry) entry() (recordstore.BatchEntry, error) {
	entry := recordstore.BatchEntry{Op: e.Op, Stream: e.Stream, Kind: e.Kind, Seal: e.Seal, Generation: e.Generation}
	if e.TTL != "" {
		ttl, err := time.ParseDuration(e.TTL)
		if err != nil {
			return recordstore.BatchEntry{}, fmt.Errorf("stream %q: ttl %q: %w", e.Stream, e.TTL, err)
		}
		entry.TTL = ttl
	}
	if e.Before != nil {
		entry.Before = *e.Before
	}
	return entry, nil
}
