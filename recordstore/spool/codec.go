// Codecs encode a batch entry's rows into its data file: ndjson and gzipped
// ndjson here, and others registered by the packages that implement them.
package spool

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sync"

	"github.com/flanksource/commons-db/recordstore"
)

const (
	FormatNDJSON     = "ndjson"
	FormatNDJSONGzip = "ndjson.gz"
)

// Codec writes and reads the rows of one data file. Decode hands numbers over
// as json.Number, so an int64 survives the trip whole.
type Codec interface {
	Encode(w io.Writer, rows []recordstore.Row) error
	Decode(r io.Reader, fn func(recordstore.Row) error) error
}

var (
	codecsMu sync.RWMutex
	codecs   = map[string]Codec{FormatNDJSON: ndjsonCodec{}, FormatNDJSONGzip: gzipCodec{}}
)

// Register makes codec the one data files of format are written and read
// with. A format is registered once; registering it again panics, as a
// program wiring two codecs to one format is broken.
func Register(format string, codec Codec) {
	codecsMu.Lock()
	defer codecsMu.Unlock()
	if _, taken := codecs[format]; taken {
		panic(fmt.Sprintf("spool: codec %q is already registered", format))
	}
	codecs[format] = codec
}

// Formats are the formats this build reads and writes, sorted.
func Formats() []string {
	codecsMu.RLock()
	defer codecsMu.RUnlock()
	return slices.Sorted(maps.Keys(codecs))
}

func codecFor(format string) (Codec, error) {
	codecsMu.RLock()
	defer codecsMu.RUnlock()
	codec, ok := codecs[format]
	if !ok {
		return nil, fmt.Errorf("spool: no codec for format %q; this build reads %q", format, slices.Sorted(maps.Keys(codecs)))
	}
	return codec, nil
}

// ndjsonCodec writes a row per line.
type ndjsonCodec struct{}

func (ndjsonCodec) Encode(w io.Writer, rows []recordstore.Row) error {
	encoder := json.NewEncoder(w)
	for index, row := range rows {
		if err := encoder.Encode(row); err != nil {
			return fmt.Errorf("row %d: %w", index, err)
		}
	}
	return nil
}

func (ndjsonCodec) Decode(r io.Reader, fn func(recordstore.Row) error) error {
	decoder := json.NewDecoder(r)
	decoder.UseNumber()
	for index := 0; ; index++ {
		var row recordstore.Row
		err := decoder.Decode(&row)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("row %d: %w", index, err)
		}
		if err := fn(row); err != nil {
			return err
		}
	}
}

// gzipCodec is ndjson in a gzip stream.
type gzipCodec struct{}

func (gzipCodec) Encode(w io.Writer, rows []recordstore.Row) error {
	compressed := gzip.NewWriter(w)
	if err := (ndjsonCodec{}).Encode(compressed, rows); err != nil {
		return errors.Join(err, compressed.Close())
	}
	return compressed.Close()
}

func (gzipCodec) Decode(r io.Reader, fn func(recordstore.Row) error) error {
	compressed, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	if err := (ndjsonCodec{}).Decode(compressed, fn); err != nil {
		return errors.Join(err, compressed.Close())
	}
	return compressed.Close()
}
