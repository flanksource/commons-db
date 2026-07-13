// Package opensearchinspect provides bounded, read-only OpenSearch metadata inspection.
package opensearchinspect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

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
}

type Options struct {
	MaxTargets int
	MaxFields  int
}

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
	return &Inspector{client: client, maxTargets: options.MaxTargets, maxFields: options.MaxFields}, nil
}

type Target struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Hidden     bool   `json:"hidden,omitempty"`
	System     bool   `json:"system,omitempty"`
	DataStream string `json:"dataStream,omitempty"`
}

type TargetCatalog struct {
	Targets        []Target `json:"targets"`
	Truncated      bool     `json:"truncated,omitempty"`
	TruncateReason string   `json:"truncateReason,omitempty"`
}

type Field struct {
	Name         string   `json:"name"`
	Types        []string `json:"types"`
	Searchable   bool     `json:"searchable"`
	Aggregatable bool     `json:"aggregatable"`
	Conflicting  bool     `json:"conflicting,omitempty"`
}

type FieldCatalog struct {
	Target         Target  `json:"target"`
	Fields         []Field `json:"fields"`
	Truncated      bool    `json:"truncated,omitempty"`
	TruncateReason string  `json:"truncateReason,omitempty"`
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

func (i *Inspector) Targets(ctx context.Context) (TargetCatalog, error) {
	response, err := i.client.Indices.ResolveIndex(
		[]string{"*"},
		i.client.Indices.ResolveIndex.WithContext(ctx),
		i.client.Indices.ResolveIndex.WithExpandWildcards("open,hidden"),
	)
	if err != nil {
		return TargetCatalog{}, fmt.Errorf("resolve opensearch indices: %w", err)
	}
	defer response.Body.Close()
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
	sort.Slice(ordered, func(a, b int) bool {
		if ordered[a].Kind != ordered[b].Kind {
			return ordered[a].Kind < ordered[b].Kind
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

func (i *Inspector) Fields(ctx context.Context, target Target) (FieldCatalog, error) {
	if target.Name == "" || !validTargetKind(target.Kind) {
		return FieldCatalog{}, fmt.Errorf("invalid opensearch target")
	}
	response, err := i.client.FieldCaps(
		i.client.FieldCaps.WithContext(ctx),
		i.client.FieldCaps.WithIndex(target.Name),
		i.client.FieldCaps.WithFields("*"),
		i.client.FieldCaps.WithIgnoreUnavailable(true),
		i.client.FieldCaps.WithAllowNoIndices(false),
	)
	if err != nil {
		return FieldCatalog{}, fmt.Errorf("inspect opensearch fields: %w", err)
	}
	defer response.Body.Close()
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

	names := make([]string, 0, len(payload.Fields))
	for name := range payload.Fields {
		names = append(names, name)
	}
	sort.Strings(names)
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
		catalog.Fields = append(catalog.Fields, Field{Name: name, Types: types, Searchable: searchable, Aggregatable: aggregatable, Conflicting: len(types) > 1})
	}
	return catalog, nil
}

func validTargetKind(kind string) bool {
	return kind == "index" || kind == "alias" || kind == "data_stream"
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
