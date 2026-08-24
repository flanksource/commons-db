// Package paramfile expands a --param value that points at a file of values.
//
// It deliberately lives under cmd/query/internal so the compiler keeps it out of
// reach of anything the server links: reading a path named by an HTTP request
// would be an arbitrary local-file read. Every caller here runs in the operator's
// own process, from their own shell.
package paramfile

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Load expands one --param value. A value that does not start with "@" comes
// back unchanged as a single element; "@path[#selector]" reads path, where the
// selector names a CSV header column or a JSON object key.
//
// A "!" prefix survives, so a file may carry exclusions and reads exactly like
// the comma-joined form typed on the command line.
func Load(value string) ([]string, error) {
	if !strings.HasPrefix(value, "@") {
		return []string{value}, nil
	}
	path, selector, _ := strings.Cut(strings.TrimPrefix(value, "@"), "#")
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("@ must be followed by a file path")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read param file: %w", err)
	}

	var values []string
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".csv":
		values, err = parseCSV(content, selector)
	case ".json":
		values, err = parseJSON(content, selector)
	case ".txt":
		values, err = parseLines(content, selector)
	default:
		return nil, fmt.Errorf("unsupported param file %q: expected .csv, .json or .txt", path)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	values = clean(values)
	if len(values) == 0 {
		return nil, fmt.Errorf("%s contained no values", path)
	}
	return values, nil
}

// parseCSV reads one column. The first record is always treated as a header, so
// a named selector can resolve; a headerless file should use .txt instead.
func parseCSV(content []byte, selector string) ([]string, error) {
	reader := csv.NewReader(strings.NewReader(string(content)))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse CSV: %w", err)
	}
	if len(records) == 0 {
		return nil, nil
	}

	header := records[0]
	index := 0
	if selector != "" {
		index = -1
		for i, name := range header {
			if strings.EqualFold(strings.TrimSpace(name), selector) {
				index = i
				break
			}
		}
		if index < 0 {
			return nil, fmt.Errorf("column %q not found (have: %s)", selector, strings.Join(header, ", "))
		}
	}

	values := make([]string, 0, len(records)-1)
	for _, record := range records[1:] {
		if index < len(record) {
			values = append(values, record[index])
		}
	}
	return values, nil
}

// parseJSON reads a flat string array, or an array of objects when a selector
// names the key to read.
func parseJSON(content []byte, selector string) ([]string, error) {
	var decoded any
	if err := json.Unmarshal(content, &decoded); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	items, ok := decoded.([]any)
	if !ok {
		return nil, fmt.Errorf("expected a JSON array, found %s", jsonKind(decoded))
	}

	values := make([]string, 0, len(items))
	for i, item := range items {
		switch typed := item.(type) {
		case string:
			if selector != "" {
				return nil, fmt.Errorf("selector %q needs an array of objects, but item %d is a string", selector, i)
			}
			values = append(values, typed)
		case map[string]any:
			if selector == "" {
				return nil, fmt.Errorf("item %d is an object; name the key to read with #key", i)
			}
			raw, present := typed[selector]
			if !present {
				return nil, fmt.Errorf("item %d has no key %q", i, selector)
			}
			text, ok := raw.(string)
			if !ok {
				return nil, fmt.Errorf("item %d key %q is %s, not a string", i, selector, jsonKind(raw))
			}
			values = append(values, text)
		default:
			return nil, fmt.Errorf("item %d is %s, not a string or an object", i, jsonKind(item))
		}
	}
	return values, nil
}

func parseLines(content []byte, selector string) ([]string, error) {
	if selector != "" {
		return nil, fmt.Errorf("a .txt file holds one value per line and takes no #selector")
	}
	return strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n"), nil
}

// clean trims, drops blanks, and dedupes while preserving first-seen order, so a
// file and a hand-typed list produce the same selection.
func clean(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case float64:
		return "a number"
	case string:
		return "a string"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// Parse turns repeatable key=value flags into the param map query.Execute takes,
// expanding any @file reference. A plain value stays a string so a scalar param
// behaves exactly as it always has; an expanded file becomes a []string.
func Parse(pairs []string) (map[string]any, error) {
	params := make(map[string]any, len(pairs))
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		if key = strings.TrimSpace(key); !ok || key == "" {
			return nil, fmt.Errorf("invalid --param %q: expected key=value", pair)
		}
		if !strings.HasPrefix(value, "@") {
			params[key] = value
			continue
		}
		values, err := Load(value)
		if err != nil {
			return nil, fmt.Errorf("param %q: %w", key, err)
		}
		params[key] = values
	}
	return params, nil
}
