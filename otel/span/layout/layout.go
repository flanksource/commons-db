package layout

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

type Direction string

const (
	ToSource    Direction = "source"
	ToCanonical Direction = "canonical"
)

type Field struct {
	Source string         `json:"source"`
	Query  string         `json:"query,omitempty"`
	Type   string         `json:"type,omitempty"`
	Unit   string         `json:"unit,omitempty"`
	Format string         `json:"format,omitempty"`
	Values map[string]any `json:"values,omitempty"`
}

type Definition struct {
	Fields map[string]Field `json:"fields"`
	// Nested resolves canonical attributes no scalar field maps, from a key/value
	// list such as Jaeger's nested `tags`, whose entries are never flattened.
	Nested []NestedAttributes `json:"nested,omitempty"`
}

// NestedAttributes maps every canonical name under Prefix to the Value of the
// entry of the nested list at Path whose Key equals the rest of the name.
type NestedAttributes struct {
	Prefix string `json:"prefix"`
	Path   string `json:"path"`
	Key    string `json:"key"`
	Value  string `json:"value"`
}

// Target is where a canonical field is read in the source index: a scalar
// Field, or Field inside the Nested list entry Where pins.
type Target struct {
	Field  string
	Nested string
	Where  map[string]string
}

// NestedLeaf is a field mapped inside a nested container.
type NestedLeaf struct {
	Type      string
	Container string
}

type Discovery struct {
	Fields map[string]string
	// Nested holds the leaves of nested containers, keyed by full field name.
	Nested          map[string]NestedLeaf
	Overrides       map[string]Field
	NestedOverrides []NestedAttributes
}

// nestedAttributeLists are the key/value lists span stores keep attributes in.
var nestedAttributeLists = []NestedAttributes{
	{Prefix: "attributes.", Path: "tags", Key: "tags.key", Value: "tags.value"},
	{Prefix: "resource.", Path: "process.tags", Key: "process.tags.key", Value: "process.tags.value"},
}

var core = []string{"trace_id", "span_id", "parent_id", "timestamp", "duration_ms", "service", "operation", "status", "kind"}

// Discover resolves a layout from the selected index's mapped fields, never its name.
func Discover(options Discovery) (Definition, error) {
	mapped := options.Fields
	if mapped["spanID"] != "" && mapped["span_id"] != "" && len(options.Overrides) == 0 {
		return Definition{}, fmt.Errorf("multiple span layouts match this index; narrow the index or specify connection span_layout")
	}
	d := Definition{Fields: map[string]Field{}}
	candidates := map[string][]string{
		"trace_id": {"trace_id", "traceId", "traceID", "trace.id"}, "span_id": {"span_id", "spanId", "spanID", "span.id"},
		"parent_id":   {"parent_id", "parentId", "parentID", "parentSpanId"},
		"timestamp":   {"timestamp", "startedAt", "startTimeMillis", "@timestamp", "startTimeUnixNano", "startTime"},
		"duration_ms": {"duration_ms", "durationMs", "durationNano", "duration_nano", "durationNanos", "duration"},
		"service":     {"service", "service_name", "serviceName", "process.serviceName", "resource.attributes.service.name"},
		"operation":   {"operation", "operation_name", "operationName", "name"}, "status": {"status.code", "status", "tag.otel@status_code", "otel.status_code", "tag.error", "error"},
		"kind": {"kind", "span.kind", "tag.span@kind"},
	}
	for _, canonical := range core {
		if _, explicit := options.Overrides[canonical]; explicit {
			continue
		}
		for _, source := range candidates[canonical] {
			if typ, ok := mapped[source]; ok {
				field := Field{Source: source, Type: typ}
				if typ == "text" {
					if mapped[source+".keyword"] == "keyword" {
						field.Query = source + ".keyword"
					}
				}
				if canonical == "duration_ms" {
					switch source {
					case "duration_ms", "durationMs":
						field.Unit = "ms"
					case "duration":
						if mapped["spanID"] != "" {
							field.Unit = "us"
						} else if mapped["span_id"] != "" && mapped["@timestamp"] != "" {
							field.Unit = "ns"
						} else {
							return d, fmt.Errorf("duration has no recognized source unit")
						}
					default:
						field.Unit = "ns"
					}
				}
				if canonical == "timestamp" && typ != "date" && typ != "date_nanos" {
					switch source {
					case "startTimeMillis":
						field.Unit = "ms"
					case "startTimeUnixNano":
						field.Unit = "ns"
					case "startTime":
						if mapped["spanID"] != "" {
							field.Unit = "us"
						} else {
							return d, fmt.Errorf("timestamp %q requires a source unit", source)
						}
					default:
						return d, fmt.Errorf("timestamp %q requires a source unit", source)
					}
				}
				if canonical == "status" {
					if source == "tag.otel@status_code" || source == "otel.status_code" {
						field.Values = map[string]any{"unset": "UNSET", "ok": "OK", "error": "ERROR"}
					}
					switch typ {
					case "boolean":
						field.Values = map[string]any{"error": true, "unset": false}
					case "integer", "long", "short", "byte":
						field.Values = map[string]any{"unset": 0, "ok": 1, "error": 2}
					}
				}
				d.Fields[canonical] = field
				break
			}
		}
	}
	for source, typ := range mapped {
		if strings.HasSuffix(source, ".keyword") {
			continue
		}
		for _, prefix := range []struct{ source, canonical string }{{"resource.attributes.", "resource."}, {"process.tag.", "resource."}, {"attributes.", "attributes."}, {"tag.", "attributes."}} {
			if suffix, ok := strings.CutPrefix(source, prefix.source); ok {
				canonical := prefix.canonical + strings.ReplaceAll(suffix, "@", ".")
				if existing, ok := d.Fields[canonical]; ok && existing.Source != source {
					return d, fmt.Errorf("ambiguous mapping for %s: %s and %s", canonical, existing.Source, source)
				}
				field := Field{Source: source, Type: typ}
				if typ == "text" && mapped[source+".keyword"] == "keyword" {
					field.Query = source + ".keyword"
				}
				d.Fields[canonical] = field
				break
			}
		}
	}
	for name, field := range options.Overrides {
		known := false
		for _, coreName := range core {
			known = known || name == coreName
		}
		if !known && !strings.HasPrefix(name, "attributes.") && !strings.HasPrefix(name, "resource.") {
			return d, fmt.Errorf("unknown canonical layout field %s", name)
		}
		if field.Source == "" {
			return d, fmt.Errorf("layout override %s requires source", name)
		}
		typ, ok := mapped[field.Source]
		if !ok {
			return d, fmt.Errorf("layout override %s uses unmapped source %s", name, field.Source)
		}
		if field.Query != "" && mapped[field.Query] == "" {
			return d, fmt.Errorf("layout override %s uses unmapped query field %s", name, field.Query)
		}
		field.Type = typ
		if field.Query == "" && typ == "text" && mapped[field.Source+".keyword"] == "keyword" {
			field.Query = field.Source + ".keyword"
		}
		if field.Unit != "" && name != "timestamp" && name != "duration_ms" {
			return d, fmt.Errorf("source units are not supported for %s", name)
		}
		if field.Unit != "" && field.Unit != "s" && field.Unit != "ms" && field.Unit != "us" && field.Unit != "ns" {
			return d, fmt.Errorf("unknown source unit %s for %s", field.Unit, name)
		}
		if name == "timestamp" && typ != "date" && typ != "date_nanos" && field.Unit == "" {
			return d, fmt.Errorf("timestamp layout override requires a source unit")
		}
		if name == "duration_ms" && field.Unit == "" && field.Source != "duration_ms" && field.Source != "durationMs" {
			return d, fmt.Errorf("duration layout override requires a source unit")
		}
		d.Fields[name] = field
	}
	for _, name := range []string{"trace_id", "span_id", "timestamp", "duration_ms"} {
		if _, ok := d.Fields[name]; !ok {
			return d, fmt.Errorf("cannot discover canonical %s from index mappings", name)
		}
	}
	nested, err := discoverNested(options)
	if err != nil {
		return d, err
	}
	d.Nested = nested
	return d, nil
}

// discoverNested finds the key/value attribute lists the index maps as nested
// keyword pairs, then validates any declared by the connection layout.
func discoverNested(options Discovery) ([]NestedAttributes, error) {
	keywordPair := func(list NestedAttributes) bool {
		key, value := options.Nested[list.Key], options.Nested[list.Value]
		return key.Type == "keyword" && key.Container == list.Path && value.Type == "keyword" && value.Container == list.Path
	}
	lists := []NestedAttributes{}
	for _, list := range nestedAttributeLists {
		if keywordPair(list) {
			lists = append(lists, list)
		}
	}
	for _, list := range options.NestedOverrides {
		if list.Prefix != "attributes." && list.Prefix != "resource." {
			return nil, fmt.Errorf("nested layout prefix %q must be attributes. or resource.", list.Prefix)
		}
		if !keywordPair(list) {
			return nil, fmt.Errorf("nested layout %s requires keyword %s and %s inside nested %s", list.Prefix, list.Key, list.Value, list.Path)
		}
		lists = slices.DeleteFunc(lists, func(existing NestedAttributes) bool { return existing.Prefix == list.Prefix })
		lists = append(lists, list)
	}
	return lists, nil
}

func (d Definition) resolve(name string, direction Direction) (string, Field, error) {
	if name == "_id" || name == "_score" || name == "_shard_doc" {
		return name, Field{Source: name}, nil
	}
	if direction == ToSource {
		field, ok := d.Fields[name]
		if !ok {
			return "", Field{}, fmt.Errorf("canonical field %q has no source mapping", name)
		}
		target := field.Source
		if field.Query != "" {
			target = field.Query
		}
		return target, field, nil
	}
	if direction != ToCanonical {
		return "", Field{}, fmt.Errorf("invalid translation direction %q", direction)
	}
	for _, canonical := range core {
		field, ok := d.Fields[canonical]
		if ok && (field.Source == name || field.Query != "" && field.Query == name) {
			return canonical, field, nil
		}
	}
	keys := []string{}
	for canonical, field := range d.Fields {
		if field.Source == name || field.Query != "" && field.Query == name {
			keys = append(keys, canonical)
		}
	}
	sort.Strings(keys)
	if len(keys) != 1 {
		return "", Field{}, fmt.Errorf("source field %q has %d canonical mappings", name, len(keys))
	}
	return keys[0], d.Fields[keys[0]], nil
}

// SourceField is the scalar source field of canonical. An attribute only a
// nested list holds has none; read it through SourceTarget.
func (d Definition) SourceField(canonical string) (string, error) {
	if _, ok := d.Fields[canonical]; !ok {
		if list, ok := d.nestedList(canonical); ok {
			return "", fmt.Errorf("canonical field %q is held in nested %s; resolve it with SourceTarget", canonical, list.Path)
		}
	}
	name, _, err := d.resolve(canonical, ToSource)
	return name, err
}

// SourceTarget is where canonical is read: its scalar field when the index maps
// one, else the value of the nested list entry keyed by the attribute name.
func (d Definition) SourceTarget(canonical string) (Target, error) {
	if _, ok := d.Fields[canonical]; !ok {
		if list, ok := d.nestedList(canonical); ok {
			return Target{Field: list.Value, Nested: list.Path, Where: map[string]string{list.Key: strings.TrimPrefix(canonical, list.Prefix)}}, nil
		}
	}
	name, _, err := d.resolve(canonical, ToSource)
	return Target{Field: name}, err
}

// nestedList is the nested attribute list holding canonical, by longest prefix.
func (d Definition) nestedList(canonical string) (NestedAttributes, bool) {
	var found NestedAttributes
	for _, list := range d.Nested {
		if strings.HasPrefix(canonical, list.Prefix) && len(canonical) > len(list.Prefix) && len(list.Prefix) > len(found.Prefix) {
			found = list
		}
	}
	return found, found.Path != ""
}

// nestedSource is the list whose Path is path, for folding a nested source
// clause back to its canonical attribute.
func (d Definition) nestedSource(path string) (NestedAttributes, bool) {
	for _, list := range d.Nested {
		if list.Path == path {
			return list, true
		}
	}
	return NestedAttributes{}, false
}
func (d Definition) CanonicalValue(canonical string, value any) (any, error) {
	field, ok := d.Fields[canonical]
	if !ok {
		return nil, fmt.Errorf("canonical field %s is unmapped", canonical)
	}
	return field.value(value, canonical, ToCanonical)
}
