// Row processing a trace kind opts into before its records are stored: cutting
// long strings, parsing JSON held in named text fields, and masking secrets.

package traces

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/flanksource/commons/logger"

	"github.com/flanksource/commons-db/recordstore"
)

// TruncatedColumn is the column WithTruncation lists the cut values' paths in.
const TruncatedColumn = "truncated"

// truncationMarker ends every value WithTruncation cut.
const truncationMarker = "…[truncated]"

// maskedValue replaces the value of every sensitive key WithSecretMasking finds.
const maskedValue = "****"

// Truncation is the column a record embeds to carry the paths of the values
// WithTruncation cut, which a kind that truncates must declare.
type Truncation struct {
	Truncated []string `json:"truncated,omitempty" pretty:"label=Truncated"`
}

// rowProcessor rewrites one encoded record.
type rowProcessor func(recordstore.Row) recordstore.Row

func pipeline(processors []rowProcessor) rowProcessor {
	return func(row recordstore.Row) recordstore.Row {
		for _, process := range processors {
			row = process(row)
		}
		return row
	}
}

// truncateStrings cuts every string longer than maxBytes, at any depth, to its
// first maxBytes at a rune boundary followed by truncationMarker, and lists the
// dotted paths of the strings it cut in TruncatedColumn. The top-level columns
// in fixed hold values of a fixed form, such as times, which are never cut.
func truncateStrings(maxBytes int, fixed []string) rowProcessor {
	return func(row recordstore.Row) recordstore.Row {
		var cut []any
		for _, key := range sortedKeys(row) {
			if key == TruncatedColumn || slices.Contains(fixed, key) {
				continue
			}
			row[key] = truncateValue(row[key], maxBytes, key, &cut)
		}
		if len(cut) > 0 {
			row[TruncatedColumn] = cut
		}
		return row
	}
}

func truncateValue(value any, maxBytes int, path string, cut *[]any) any {
	switch v := value.(type) {
	case string:
		if len(v) <= maxBytes {
			return v
		}
		end := maxBytes
		for end > 0 && !utf8.RuneStart(v[end]) {
			end--
		}
		*cut = append(*cut, path)
		return v[:end] + truncationMarker
	case map[string]any:
		for _, key := range sortedKeys(v) {
			v[key] = truncateValue(v[key], maxBytes, path+"."+key, cut)
		}
		return v
	case []any:
		for index := range v {
			v[index] = truncateValue(v[index], maxBytes, path+"."+strconv.Itoa(index), cut)
		}
		return v
	}
	return value
}

// parseJSONAt replaces the string at each dotted path that holds a JSON object
// or array with its decoded value, keeping numbers as json.Number as
// recordstore.EncodeRow does. No other string is parsed, and a path the row
// does not reach is skipped.
func parseJSONAt(paths []string) rowProcessor {
	return func(row recordstore.Row) recordstore.Row {
		for _, path := range paths {
			updateAt(row, path, parseJSONText)
		}
		return row
	}
}

func parseJSONText(value any) any {
	text, ok := value.(string)
	if !ok {
		return value
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') || !json.Valid([]byte(trimmed)) {
		return text
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(trimmed)))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return text
	}
	return decoded
}

// jsonAsText encodes the structure at each dotted path back into JSON text,
// so a record whose text parseJSONAt parsed decodes into its own type again.
func jsonAsText(row recordstore.Row, paths []string) {
	for _, path := range paths {
		updateAt(row, path, func(value any) any {
			if _, ok := value.(string); ok || value == nil {
				return value
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				return value
			}
			return string(encoded)
		})
	}
}

// updateAt replaces the value at the dotted path through row's objects with
// update's, if the row has one there.
func updateAt(row recordstore.Row, path string, update func(any) any) {
	keys := strings.Split(path, ".")
	parent := map[string]any(row)
	for _, key := range keys[:len(keys)-1] {
		child, ok := parent[key].(map[string]any)
		if !ok {
			return
		}
		parent = child
	}
	last := keys[len(keys)-1]
	if value, ok := parent[last]; ok {
		parent[last] = update(value)
	}
}

// maskSecrets replaces the value of every sensitive key (logger.IsSensitiveLogKey)
// at any depth, and the value of every {name, value} pair whose name is
// sensitive (HAR headers, cookies and query strings), with maskedValue; every
// other string has the secrets inside it stripped (logger.StripSecrets). Keys
// named in keep are never masked, at any depth: they are columns whose names
// only look sensitive, such as a session id.
func maskSecrets(keep []string) rowProcessor {
	masker := secretMasker{keep: keep}
	return func(row recordstore.Row) recordstore.Row {
		for key, value := range row {
			row[key] = masker.entry(key, value)
		}
		return row
	}
}

type secretMasker struct{ keep []string }

func (m secretMasker) entry(key string, value any) any {
	if slices.Contains(m.keep, key) {
		return value
	}
	if logger.IsSensitiveLogKey(key) && value != nil {
		return maskedValue
	}
	return m.value(value)
}

func (m secretMasker) value(value any) any {
	switch v := value.(type) {
	case string:
		return logger.StripSecrets(v)
	case map[string]any:
		if name, ok := v["name"].(string); ok && len(v) <= 2 && logger.IsSensitiveLogKey(name) {
			if _, ok := v["value"]; ok {
				v["value"] = maskedValue
				return v
			}
		}
		for key, nested := range v {
			v[key] = m.entry(key, nested)
		}
		return v
	case []any:
		for index, nested := range v {
			v[index] = m.value(nested)
		}
		return v
	}
	return value
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
