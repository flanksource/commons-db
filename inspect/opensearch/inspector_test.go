package opensearchinspect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	opensearch "github.com/opensearch-project/opensearch-go/v2"
)

func TestInspectorTargetsAndFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_resolve/index/*":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"indices":[{"name":"logs-2","aliases":["logs"],"attributes":[]},{"name":".system","attributes":["hidden"]}],"aliases":[{"name":"logs","indices":["logs-2"]}],"data_streams":[{"name":"traces","backing_indices":[".ds-traces-1"]}]}`))
		case "/logs/_field_caps":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"fields":{"service.name":{"keyword":{"searchable":true,"aggregatable":true}},"duration":{"long":{"searchable":true,"aggregatable":true},"double":{"searchable":true,"aggregatable":true}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := opensearch.NewClient(opensearch.Config{Addresses: []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	inspector, err := New(client, Options{})
	if err != nil {
		t.Fatal(err)
	}
	targets, err := inspector.Targets(context.Background(), TargetRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets.Targets) != 4 || targets.Targets[0].Kind != "alias" {
		t.Fatalf("targets = %#v", targets)
	}
	fields, err := inspector.Fields(context.Background(), FieldRequest{Target: Target{Name: "logs", Kind: "alias"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fields.Fields) != 2 || !fields.Fields[0].Conflicting || fields.Fields[1].Name != "service.name" {
		t.Fatalf("fields = %#v", fields)
	}
}

func TestInspectorRequestsOnlyNamedFields(t *testing.T) {
	var requested string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = r.URL.Query().Get("fields")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"fields":{"startTimeMillis":{"long":{"searchable":true,"aggregatable":true}}}}`))
	}))
	defer server.Close()
	client, err := opensearch.NewClient(opensearch.Config{Addresses: []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	inspector, err := New(client, Options{})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := inspector.Fields(context.Background(), FieldRequest{
		Target: Target{Name: "traces-*", Kind: "pattern"},
		Names:  []string{"startTimeMillis"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if requested != "startTimeMillis" {
		t.Fatalf("field_caps fields = %q, want startTimeMillis", requested)
	}
	if len(catalog.Fields) != 1 || catalog.Fields[0].Name != "startTimeMillis" || catalog.Fields[0].Types[0] != "long" {
		t.Fatalf("catalog = %#v", catalog)
	}
}

func TestInspectorCarriesNamedFieldMappingFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/traces-*/_field_caps":
			_, _ = w.Write([]byte(`{"fields":{"startTimeMillis":{"date":{"searchable":true,"aggregatable":true}}}}`))
		case "/traces-*/_mapping/field/startTimeMillis":
			if r.URL.Query().Get("include_defaults") != "true" {
				t.Errorf("include_defaults = %q, want true", r.URL.Query().Get("include_defaults"))
			}
			_, _ = w.Write([]byte(`{"traces-2026":{"mappings":{"startTimeMillis":{"full_name":"startTimeMillis","mapping":{"startTimeMillis":{"type":"date","format":"epoch_millis"}}}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := opensearch.NewClient(opensearch.Config{Addresses: []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	inspector, err := New(client, Options{})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := inspector.Fields(context.Background(), FieldRequest{
		Target: Target{Name: "traces-*", Kind: "pattern"}, Names: []string{"startTimeMillis"}, IncludeFormats: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Fields) != 1 || catalog.Fields[0].Format != "epoch_millis" || catalog.Fields[0].FormatConflicting {
		t.Fatalf("catalog = %#v", catalog)
	}
}

// The mapping is the only thing that tells a nested tag list apart from a plain
// array of objects: their leaves are reported identically, and a selection that
// reads one as the other either matches nothing or matches the wrong documents.
func TestInspectorCarriesContainerToLeaves(t *testing.T) {
	const caps = `{"fields":{
		"tags":{"nested":{"searchable":false,"aggregatable":false}},
		"tags.key":{"keyword":{"searchable":true,"aggregatable":true}},
		"tags.value":{"keyword":{"searchable":true,"aggregatable":true}},
		"otel":{"object":{"searchable":false,"aggregatable":false}},
		"otel.key":{"keyword":{"searchable":true,"aggregatable":true}},
		"labels":{"object":{"searchable":false,"aggregatable":false}},
		"labels.app":{"text":{"searchable":true,"aggregatable":false}},
		"labels.app.keyword":{"keyword":{"searchable":true,"aggregatable":true}},
		"attrs":{"flat_object":{"searchable":true,"aggregatable":false}},
		"spans":{"nested":{"searchable":false,"aggregatable":false}},
		"spans.process":{"object":{"searchable":false,"aggregatable":false}},
		"spans.process.name":{"keyword":{"searchable":true,"aggregatable":true}},
		"mixed":{"object":{"searchable":false,"aggregatable":false},"nested":{"searchable":false,"aggregatable":false}},
		"mixed.key":{"keyword":{"searchable":true,"aggregatable":true}},
		"message":{"text":{"searchable":true,"aggregatable":false}}
	}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/logs/_field_caps" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(caps))
	}))
	defer server.Close()
	client, err := opensearch.NewClient(opensearch.Config{Addresses: []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	inspector, err := New(client, Options{})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := inspector.Fields(context.Background(), FieldRequest{Target: Target{Name: "logs", Kind: "index"}})
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]Field, len(catalog.Fields))
	for _, field := range catalog.Fields {
		byName[field.Name] = field
	}

	for name, want := range map[string]Field{
		"tags.key":           {Container: "tags", ContainerType: ContainerNested},
		"otel.key":           {Container: "otel", ContainerType: ContainerObject},
		"labels.app":         {Container: "labels", ContainerType: ContainerObject},
		"labels.app.keyword": {Container: "labels", ContainerType: ContainerObject},
		"spans.process.name": {Container: "spans.process", ContainerType: ContainerObject},
		"mixed.key":          {Container: "", ContainerType: ""},
		"message":            {Container: "", ContainerType: ""},
		"attrs":              {Container: "", ContainerType: ""},
	} {
		got := byName[name]
		if got.Container != want.Container || got.ContainerType != want.ContainerType {
			t.Errorf("%s: container = %q/%q, want %q/%q",
				name, got.Container, got.ContainerType, want.Container, want.ContainerType)
		}
	}
	if !byName["tags.key"].Nested() || byName["otel.key"].Nested() {
		t.Errorf("only a leaf under a nested mapping is nested: %#v %#v", byName["tags.key"], byName["otel.key"])
	}
}

func TestInspectorRejectsInvalidTarget(t *testing.T) {
	client, _ := opensearch.NewClient(opensearch.Config{Addresses: []string{"http://127.0.0.1:1"}})
	inspector, _ := New(client, Options{})
	if _, err := inspector.Fields(context.Background(), FieldRequest{Target: Target{Name: "*", Kind: "wildcard"}}); err == nil {
		t.Fatal("invalid target must fail before making a request")
	}
}
