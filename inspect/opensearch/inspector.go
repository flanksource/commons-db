// Package opensearchinspect provides bounded, read-only OpenSearch metadata inspection.
package opensearchinspect

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	inspection "github.com/flanksource/commons-db/inspect"
	opensearch "github.com/opensearch-project/opensearch-go/v2"
)

const (
	DefaultMaxTargets = 2000
	DefaultMaxFields  = 20000
)

type Inspector struct {
	client     *opensearch.Client
	maxTargets int
	maxFields  int
	cacheKey   string
}

type Options struct {
	MaxTargets int
	MaxFields  int
	CacheKey   string
}

var (
	targetCache = inspection.NewMemo(inspection.MemoOptions[TargetCatalog]{
		Policy: inspection.Policy(inspection.CacheClassOpenSearchTargets),
		Weight: func(catalog TargetCatalog) int { return len(catalog.Targets) },
	})
	fieldCache = inspection.NewMemo(inspection.MemoOptions[FieldCatalog]{
		Policy: inspection.Policy(inspection.CacheClassOpenSearchFields),
		Weight: func(catalog FieldCatalog) int { return len(catalog.Fields) },
	})
	dynamicMappingCache = inspection.NewMemo(inspection.MemoOptions[FieldCatalog]{
		Policy: inspection.Policy(inspection.CacheClassOpenSearchDynamicMapping),
		Weight: func(catalog FieldCatalog) int { return len(catalog.Fields) },
	})
	concreteMappingCache = inspection.NewMemo(inspection.MemoOptions[FieldCatalog]{
		Policy: inspection.Policy(inspection.CacheClassOpenSearchConcreteMapping),
		Weight: func(catalog FieldCatalog) int { return len(catalog.Fields) },
	})
)

func New(client *opensearch.Client, options Options) (*Inspector, error) {
	if client == nil {
		return nil, fmt.Errorf("nil opensearch client")
	}
	if options.MaxTargets <= 0 {
		options.MaxTargets = DefaultMaxTargets
	}
	if options.MaxFields <= 0 {
		options.MaxFields = DefaultMaxFields
	}
	return &Inspector{
		client: client, maxTargets: options.MaxTargets, maxFields: options.MaxFields,
		cacheKey: options.CacheKey,
	}, nil
}

type Target struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Hidden     bool   `json:"hidden,omitempty"`
	System     bool   `json:"system,omitempty"`
	DataStream string `json:"dataStream,omitempty"`
	// Pattern names the wildcard target a rotated index rolls up into.
	Pattern string `json:"pattern,omitempty"`
	// Count is how many rotations a `pattern` target covers.
	Count   int      `json:"count,omitempty"`
	Members []string `json:"members,omitempty"`
}

func (t Target) QueryName() string {
	if t.Kind == "group" {
		return strings.Join(t.Members, ",")
	}
	return t.Name
}

type TargetCatalog struct {
	Targets        []Target                  `json:"targets"`
	Truncated      bool                      `json:"truncated,omitempty"`
	TruncateReason string                    `json:"truncateReason,omitempty"`
	Cache          *inspection.CacheMetadata `json:"cache,omitempty"`
}

type TargetRequest struct {
	Refresh bool
}

type Field struct {
	Name         string   `json:"name"`
	Types        []string `json:"types"`
	Searchable   bool     `json:"searchable"`
	Aggregatable bool     `json:"aggregatable"`
	Conflicting  bool     `json:"conflicting,omitempty"`
	Format       string   `json:"format,omitempty"`
	// FormatConflicting reports that indices behind the target map this field
	// with different date formats.
	FormatConflicting bool `json:"formatConflicting,omitempty"`

	// Container names the innermost object, nested or flat_object ancestor this
	// field sits inside, and ContainerType is how that ancestor is mapped.
	//
	// They are carried down because the leaf alone cannot say how a selection on
	// it behaves: `_field_caps` reports the key of a `nested` tag list and the key
	// of a plain array of objects identically — both `keyword`, both searchable,
	// both aggregatable — and the two need opposite treatment. Only the ancestor's
	// own entry distinguishes them.
	Container     string `json:"container,omitempty"`
	ContainerType string `json:"containerType,omitempty"`
}

// Container mapping types. A field's ancestor is reduced to one of these, or to
// the empty string when the field sits at the root of the document.
const (
	ContainerObject     = "object"
	ContainerNested     = "nested"
	ContainerFlatObject = "flat_object"
)

// Nested reports that a selection on this field must be compiled inside a nested
// query to mean anything: OpenSearch indexes each element of a `nested` field as
// its own document, so a flat clause on `tags.key` matches no parent document at
// all — silently, with no error to read.
func (f Field) Nested() bool { return f.ContainerType == ContainerNested }

type FieldCatalog struct {
	Target         Target                    `json:"target"`
	Fields         []Field                   `json:"fields"`
	Truncated      bool                      `json:"truncated,omitempty"`
	TruncateReason string                    `json:"truncateReason,omitempty"`
	Cache          *inspection.CacheMetadata `json:"cache,omitempty"`
}

// FieldRequest selects one target and optionally narrows its field catalog.
type FieldRequest struct {
	Target         Target
	Names          []string
	IncludeFormats bool
	Refresh        bool
}

type resolveResponse struct {
	Indices []struct {
		Name       string   `json:"name"`
		Aliases    []string `json:"aliases"`
		Attributes []string `json:"attributes"`
		DataStream string   `json:"data_stream"`
	} `json:"indices"`
	Aliases []struct {
		Name string `json:"name"`
	} `json:"aliases"`
	DataStreams []struct {
		Name string `json:"name"`
	} `json:"data_streams"`
}

func (i *Inspector) Targets(ctx context.Context, request TargetRequest) (TargetCatalog, error) {
	if i.cacheKey == "" {
		return i.loadTargets(ctx)
	}
	result, err := targetCache.Get(ctx, inspection.GetOptions[TargetCatalog]{
		Key:     cacheKey(i.cacheKey, "targets", struct{ MaxTargets int }{MaxTargets: i.maxTargets}),
		Refresh: request.Refresh,
		Load:    i.loadTargets,
	})
	if err != nil && !result.Cache.Cached {
		return TargetCatalog{}, err
	}
	result.Value.Cache = &result.Cache
	return result.Value, nil
}

func (i *Inspector) loadTargets(ctx context.Context) (TargetCatalog, error) {
	response, err := i.client.Indices.ResolveIndex(
		[]string{"*"},
		i.client.Indices.ResolveIndex.WithContext(ctx),
		i.client.Indices.ResolveIndex.WithExpandWildcards("open,hidden"),
	)
	if err != nil {
		return TargetCatalog{}, fmt.Errorf("resolve opensearch indices: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.IsError() {
		return TargetCatalog{}, responseError("resolve opensearch indices", response.Status(), response.Body)
	}
	var payload resolveResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return TargetCatalog{}, fmt.Errorf("decode opensearch targets: %w", err)
	}

	targets := make(map[string]Target)
	for _, item := range payload.Indices {
		hidden := false
		for _, attribute := range item.Attributes {
			hidden = hidden || attribute == "hidden"
		}
		targets[targetKey("index", item.Name)] = Target{Name: item.Name, Kind: "index", Hidden: hidden, System: strings.HasPrefix(item.Name, "."), DataStream: item.DataStream}
		for _, alias := range item.Aliases {
			targets[targetKey("alias", alias)] = Target{Name: alias, Kind: "alias", System: strings.HasPrefix(alias, ".")}
		}
	}
	for _, item := range payload.Aliases {
		targets[targetKey("alias", item.Name)] = Target{Name: item.Name, Kind: "alias", System: strings.HasPrefix(item.Name, ".")}
	}
	for _, item := range payload.DataStreams {
		targets[targetKey("data_stream", item.Name)] = Target{Name: item.Name, Kind: "data_stream", System: strings.HasPrefix(item.Name, ".")}
	}

	ordered := make([]Target, 0, len(targets))
	for _, target := range targets {
		ordered = append(ordered, target)
	}
	ordered = RollupTargets(ordered)
	// Patterns lead, so a rotation survives the target limit even when its
	// thousands of daily indexes do not.
	sort.Slice(ordered, func(a, b int) bool {
		if kindRank(ordered[a].Kind) != kindRank(ordered[b].Kind) {
			return kindRank(ordered[a].Kind) < kindRank(ordered[b].Kind)
		}
		return ordered[a].Name < ordered[b].Name
	})
	catalog := TargetCatalog{Targets: ordered}
	if len(catalog.Targets) > i.maxTargets {
		catalog.Targets = catalog.Targets[:i.maxTargets]
		catalog.Truncated = true
		catalog.TruncateReason = fmt.Sprintf("target limit %d reached", i.maxTargets)
	}
	return catalog, nil
}

func (i *Inspector) Fields(ctx context.Context, request FieldRequest) (FieldCatalog, error) {
	if i.cacheKey == "" {
		return i.loadFields(ctx, request)
	}
	names := append([]string(nil), request.Names...)
	sort.Strings(names)
	key := cacheKey(i.cacheKey, "fields", struct {
		Target         Target
		Names          []string
		IncludeFormats bool
		MaxFields      int
	}{Target: request.Target, Names: names, IncludeFormats: request.IncludeFormats, MaxFields: i.maxFields})
	cache := fieldCache
	if len(names) > 0 {
		cache = dynamicMappingCache
		if request.Target.Kind == "index" && !strings.Contains(request.Target.Name, "*") {
			cache = concreteMappingCache
		}
	}
	result, err := cache.Get(ctx, inspection.GetOptions[FieldCatalog]{
		Key:     key,
		Refresh: request.Refresh,
		Load: func(loadContext context.Context) (FieldCatalog, error) {
			return i.loadFields(loadContext, request)
		},
	})
	if err != nil && !result.Cache.Cached {
		return FieldCatalog{}, err
	}
	result.Value.Cache = &result.Cache
	return result.Value, nil
}

func (i *Inspector) loadFields(ctx context.Context, request FieldRequest) (FieldCatalog, error) {
	target := request.Target
	if target.Name == "" || !validTargetKind(target.Kind) {
		return FieldCatalog{}, fmt.Errorf("invalid opensearch target")
	}
	fields := request.Names
	if len(fields) == 0 {
		fields = []string{"*"}
	}
	response, err := i.client.FieldCaps(
		i.client.FieldCaps.WithContext(ctx),
		i.client.FieldCaps.WithIndex(target.QueryName()),
		i.client.FieldCaps.WithFields(fields...),
		i.client.FieldCaps.WithIgnoreUnavailable(true),
		i.client.FieldCaps.WithAllowNoIndices(false),
	)
	if err != nil {
		return FieldCatalog{}, fmt.Errorf("inspect opensearch fields: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.IsError() {
		return FieldCatalog{}, responseError("inspect opensearch fields", response.Status(), response.Body)
	}
	var payload struct {
		Fields map[string]map[string]struct {
			Searchable   bool `json:"searchable"`
			Aggregatable bool `json:"aggregatable"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return FieldCatalog{}, fmt.Errorf("decode opensearch fields: %w", err)
	}
	formats := map[string]fieldFormat{}
	if request.IncludeFormats {
		formats, err = i.fieldFormats(ctx, target, request.Names)
		if err != nil {
			return FieldCatalog{}, err
		}
	}

	names := make([]string, 0, len(payload.Fields))
	for name := range payload.Fields {
		names = append(names, name)
	}
	sort.Strings(names)
	// The container index is built from every field the mapping reports, not from
	// the ones that survive the limit: a truncated ancestor would leave its leaves
	// looking like root fields, which is the one reading that makes a nested tag
	// list compile to a clause that matches nothing.
	containers := containerTypes(names, func(name string) []string {
		types := make([]string, 0, len(payload.Fields[name]))
		for typ := range payload.Fields[name] {
			types = append(types, typ)
		}
		return types
	})
	catalog := FieldCatalog{Target: target, Fields: make([]Field, 0, min(len(names), i.maxFields))}
	for _, name := range names {
		if len(catalog.Fields) >= i.maxFields {
			catalog.Truncated = true
			catalog.TruncateReason = fmt.Sprintf("field limit %d reached", i.maxFields)
			break
		}
		caps := payload.Fields[name]
		types := make([]string, 0, len(caps))
		searchable, aggregatable := len(caps) > 0, len(caps) > 0
		for typ, capability := range caps {
			types = append(types, typ)
			searchable = searchable && capability.Searchable
			aggregatable = aggregatable && capability.Aggregatable
		}
		sort.Strings(types)
		field := Field{Name: name, Types: types, Searchable: searchable, Aggregatable: aggregatable, Conflicting: len(types) > 1}
		field.Format = formats[name].value
		field.FormatConflicting = formats[name].conflicting
		field.Container, field.ContainerType = innermostContainer(name, containers)
		catalog.Fields = append(catalog.Fields, field)
	}
	return catalog, nil
}

func cacheKey(identity, kind string, value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("encode OpenSearch inspection cache key: %s", err))
	}
	digest := sha256.Sum256(append([]byte(identity+"\x00"+kind+"\x00"), payload...))
	return fmt.Sprintf("%x", digest)
}

type fieldFormat struct {
	value       string
	conflicting bool
}

func (i *Inspector) fieldFormats(ctx context.Context, target Target, names []string) (map[string]fieldFormat, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("named fields are required to inspect OpenSearch mapping formats")
	}
	response, err := i.client.Indices.GetFieldMapping(
		names,
		i.client.Indices.GetFieldMapping.WithContext(ctx),
		i.client.Indices.GetFieldMapping.WithIndex(target.QueryName()),
		i.client.Indices.GetFieldMapping.WithIgnoreUnavailable(true),
		i.client.Indices.GetFieldMapping.WithAllowNoIndices(false),
		i.client.Indices.GetFieldMapping.WithIncludeDefaults(true),
	)
	if err != nil {
		return nil, fmt.Errorf("inspect opensearch field mappings: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.IsError() {
		return nil, responseError("inspect opensearch field mappings", response.Status(), response.Body)
	}
	var payload map[string]struct {
		Mappings map[string]struct {
			FullName string `json:"full_name"`
			Mapping  map[string]struct {
				Format string `json:"format"`
			} `json:"mapping"`
		} `json:"mappings"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode opensearch field mappings: %w", err)
	}

	values := make(map[string]map[string]struct{}, len(names))
	for _, index := range payload {
		for name, field := range index.Mappings {
			if field.FullName != "" {
				name = field.FullName
			}
			for _, mapping := range field.Mapping {
				if values[name] == nil {
					values[name] = map[string]struct{}{}
				}
				values[name][mapping.Format] = struct{}{}
			}
		}
	}
	formats := make(map[string]fieldFormat, len(values))
	for name, distinct := range values {
		if len(distinct) != 1 {
			formats[name] = fieldFormat{conflicting: true}
			continue
		}
		for value := range distinct {
			formats[name] = fieldFormat{value: value}
		}
	}
	return formats, nil
}

// containerTypes indexes the fields that hold other fields by how they are
// mapped. A field reporting several types is left out: an ancestor that is a
// nested list in one index behind a pattern and a plain object in another has no
// single answer, and guessing one is how a selection ends up meaning different
// things per index.
func containerTypes(names []string, typesOf func(string) []string) map[string]string {
	containers := make(map[string]string)
	for _, name := range names {
		types := typesOf(name)
		if len(types) != 1 {
			continue
		}
		switch types[0] {
		case ContainerObject, ContainerNested, ContainerFlatObject:
			containers[name] = types[0]
		}
	}
	return containers
}

// innermostContainer names the closest ancestor of name that holds other fields.
// Closest rather than outermost: `a.b.c` inside `a` (object) and `a.b` (nested)
// is reached through the nested one, and it is the nested one that decides how a
// clause on it must be written.
func innermostContainer(name string, containers map[string]string) (string, string) {
	for cut := strings.LastIndex(name, "."); cut > 0; cut = strings.LastIndex(name[:cut], ".") {
		if kind, ok := containers[name[:cut]]; ok {
			return name[:cut], kind
		}
	}
	return "", ""
}

// kindRank orders the target kinds from most to least useful to pick.
func kindRank(kind string) int {
	switch kind {
	case "group":
		return 0
	case "pattern":
		return 1
	case "alias":
		return 2
	case "data_stream":
		return 3
	default:
		return 4
	}
}

func validTargetKind(kind string) bool {
	return kind == "index" || kind == "alias" || kind == "data_stream" || kind == "pattern" || kind == "group"
}

func targetKey(kind, name string) string { return kind + ":" + name }

func responseError(operation, status string, body io.Reader) error {
	data, _ := io.ReadAll(io.LimitReader(body, 4096))
	message := strings.TrimSpace(string(data))
	if message == "" {
		message = status
	}
	return fmt.Errorf("%s failed with status %s: %s", operation, status, message)
}
