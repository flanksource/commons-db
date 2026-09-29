// Publishing a batch: its data files and manifest are written and made
// durable under tmp/, then the batch directory is renamed into incoming/.
package spool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/commons-db/recordstore"
)

// Digest is the SHA256 a manifest records for a data file's bytes.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Publisher delivers a batch by publishing it in format, for a Writer.
func (d Dir) Publisher(format string) func(context.Context, recordstore.Batch) error {
	return func(_ context.Context, batch recordstore.Batch) error {
		_, err := d.Publish(batch, format)
		return err
	}
}

// Publish writes batch into the spool, its rows in format, and returns the
// name it was published under in incoming/. Every kind the batch appends to
// must have its schema in batch.Schemas: the rows are normalized against it,
// and the owner ingests them under it. Nothing is published unless all of it
// is.
func (d Dir) Publish(batch recordstore.Batch, format string) (string, error) {
	if err := validIdentity(batch.ID, batch.Producer.Instance); err != nil {
		return "", err
	}
	codec, err := codecFor(format)
	if err != nil {
		return "", err
	}
	schemas := make(map[string]recordstore.KindSchema, len(batch.Schemas))
	for _, schema := range batch.Schemas {
		schemas[schema.Kind] = schema
	}
	created := time.Now().UTC()
	stage := filepath.Join(d.path, tmpDir, batch.Producer.Instance+"-"+batch.ID)
	if err := os.Mkdir(stage, 0o700); err != nil {
		return "", fmt.Errorf("spool %s: stage batch %q: %w", d.path, batch.ID, err)
	}
	name, err := d.publishStaged(stage, batch, schemas, codec, format, created)
	if err != nil {
		return "", errors.Join(fmt.Errorf("spool %s: batch %q: %w", d.path, batch.ID, err), os.RemoveAll(stage))
	}
	return name, nil
}

func (d Dir) publishStaged(stage string, batch recordstore.Batch, schemas map[string]recordstore.KindSchema,
	codec Codec, format string, created time.Time,
) (string, error) {
	written := manifest{Format: ManifestFormat, ID: batch.ID, Producer: batch.Producer, Created: created}
	for _, schema := range batch.Schemas {
		written.Schemas = append(written.Schemas, schemaOf(schema))
	}
	for index, entry := range batch.Entries {
		encoded := entryOf(entry)
		if entry.Op == recordstore.BatchAppend && len(entry.Rows) > 0 {
			schema, ok := schemas[entry.Kind]
			if !ok {
				return "", fmt.Errorf("entry %d appends kind %q, whose schema the batch does not carry", index, entry.Kind)
			}
			rows := make([]recordstore.Row, len(entry.Rows))
			for at, row := range entry.Rows {
				normalized, err := Normalize(schema, row)
				if err != nil {
					return "", fmt.Errorf("entry %d row %d: %w", index, at, err)
				}
				rows[at] = normalized
			}
			encoded.File, encoded.Format, encoded.Rows = fmt.Sprintf("%04d.%s", index, format), format, len(rows)
			digest, err := writeDataFile(filepath.Join(stage, encoded.File), codec, rows)
			if err != nil {
				return "", fmt.Errorf("entry %d: %w", index, err)
			}
			encoded.SHA256 = digest
		}
		written.Entries = append(written.Entries, encoded)
	}
	description, err := json.MarshalIndent(written, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeDurable(filepath.Join(stage, manifestFile), description); err != nil {
		return "", err
	}
	if err := syncDir(stage); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%020d-%s-%012d-%s", created.UnixNano(), batch.Producer.Instance, batch.Producer.Seq, batch.ID)
	incoming := filepath.Join(d.path, incomingDir)
	if err := os.Rename(stage, filepath.Join(incoming, name)); err != nil {
		return "", err
	}
	return name, syncDir(incoming)
}

// writeDataFile encodes rows into a durable file at path and returns its
// digest.
func writeDataFile(path string, codec Codec, rows []recordstore.Row) (string, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	if err := codec.Encode(io.MultiWriter(file, digest), rows); err != nil {
		return "", errors.Join(err, file.Close())
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func writeDurable(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	return errors.Join(file.Sync(), file.Close())
}
