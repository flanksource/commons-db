// Loading a published batch back: the manifest is checked, every data file is
// verified against its digest and row count, and the rows are decoded.
package spool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

// Load reads the published batch name. An error means the batch as a whole
// cannot be ingested — ErrManifestFormat for a manifest format this build does
// not read — and belongs in failed/.
func (d Dir) Load(name string) (recordstore.Batch, error) {
	if err := validName(name); err != nil {
		return recordstore.Batch{}, err
	}
	path := filepath.Join(d.path, incomingDir, name)
	batch, err := load(path)
	if err != nil {
		return recordstore.Batch{}, fmt.Errorf("spool %s: batch %s: %w", d.path, name, err)
	}
	return batch, nil
}

func load(path string) (recordstore.Batch, error) {
	encoded, err := os.ReadFile(filepath.Join(path, manifestFile))
	if err != nil {
		return recordstore.Batch{}, err
	}
	var read manifest
	if err := json.Unmarshal(encoded, &read); err != nil {
		return recordstore.Batch{}, fmt.Errorf("decode manifest: %w", err)
	}
	if read.Format != ManifestFormat {
		return recordstore.Batch{}, fmt.Errorf("manifest format %d, this build reads %d: %w", read.Format, ManifestFormat, ErrManifestFormat)
	}
	if err := validIdentity(read.ID, read.Producer.Instance); err != nil {
		return recordstore.Batch{}, err
	}
	batch := recordstore.Batch{ID: read.ID, Producer: read.Producer}
	types := map[string]map[string]query.ColumnType{}
	for _, encoded := range read.Schemas {
		schema, err := encoded.schema()
		if err != nil {
			return recordstore.Batch{}, err
		}
		batch.Schemas = append(batch.Schemas, schema)
		types[schema.Kind] = columnTypes(schema)
	}
	for index, encoded := range read.Entries {
		entry, err := encoded.entry()
		if err != nil {
			return recordstore.Batch{}, fmt.Errorf("entry %d: %w", index, err)
		}
		if encoded.File != "" || encoded.Rows != 0 {
			if entry.Rows, err = readDataFile(path, encoded, types[encoded.Kind]); err != nil {
				return recordstore.Batch{}, fmt.Errorf("entry %d: %w", index, err)
			}
		}
		batch.Entries = append(batch.Entries, entry)
	}
	return batch, nil
}

// readDataFile reads an entry's rows, refusing a file that is not the one the
// manifest describes.
func readDataFile(path string, entry manifestEntry, types map[string]query.ColumnType) ([]recordstore.Row, error) {
	if entry.File == "" || !filepath.IsLocal(entry.File) || filepath.Base(entry.File) != entry.File {
		return nil, fmt.Errorf("data file %q is not a file of the batch", entry.File)
	}
	codec, err := codecFor(entry.Format)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(path, entry.File))
	if err != nil {
		return nil, err
	}
	if digest := Digest(data); digest != entry.SHA256 {
		return nil, fmt.Errorf("data file %s has sha256 %s, the manifest records %s", entry.File, digest, entry.SHA256)
	}
	rows := make([]recordstore.Row, 0, entry.Rows)
	err = codec.Decode(bytes.NewReader(data), func(row recordstore.Row) error {
		if err := denormalize(types, row); err != nil {
			return fmt.Errorf("row %d: %w", len(rows), err)
		}
		rows = append(rows, row)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("data file %s: %w", entry.File, err)
	}
	if len(rows) != entry.Rows {
		return nil, fmt.Errorf("data file %s holds %d rows, the manifest records %d", entry.File, len(rows), entry.Rows)
	}
	return rows, nil
}
