// Compressed columns: a column stored as a zstd blob of its text, and the
// rs_inflate SQL function every connection reads it back through.
package sqlitetable

import (
	"bytes"
	"database/sql/driver"
	"fmt"
	"slices"

	"github.com/klauspost/compress/zstd"
	"modernc.org/sqlite"

	"github.com/flanksource/commons-db/query"
)

// inflateFunction is the SQL function that reads a compressed column back as
// its text; a value that is not a zstd blob — stored before its column was
// compressed, or NULL — passes through unchanged.
const inflateFunction = "rs_inflate"

// zstdMagic begins every zstd frame.
var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

// columnEncoder and columnDecoder are safe for concurrent EncodeAll and
// DecodeAll.
var (
	columnEncoder, _ = zstd.NewWriter(nil)
	columnDecoder, _ = zstd.NewReader(nil)
)

func init() {
	if err := sqlite.RegisterDeterministicScalarFunction(inflateFunction, 1, inflate); err != nil {
		panic(fmt.Sprintf("sqlitetable: register %s: %v", inflateFunction, err))
	}
}

func inflate(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	blob, ok := args[0].([]byte)
	if !ok || !bytes.HasPrefix(blob, zstdMagic) {
		return args[0], nil
	}
	text, err := columnDecoder.DecodeAll(blob, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", inflateFunction, err)
	}
	return string(text), nil
}

// compressed reports whether column is stored compressed.
func (t Table) compressed(column string) bool { return slices.Contains(t.Compressed, column) }

// storage is the SQLite type column is stored as: a blob when compressed.
func (t Table) storage(column query.ColumnDef) string {
	if t.compressed(column.Name) {
		return "BLOB"
	}
	return Type(column.Type)
}

// deflate is value as a compressed column stores it: the zstd blob of its text.
func deflate(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return columnEncoder.EncodeAll([]byte(typed), nil)
	case []byte:
		return columnEncoder.EncodeAll(typed, nil)
	default:
		return columnEncoder.EncodeAll(fmt.Append(nil, typed), nil)
	}
}
