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
	targets, err := inspector.Targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(targets.Targets) != 4 || targets.Targets[0].Kind != "alias" {
		t.Fatalf("targets = %#v", targets)
	}
	fields, err := inspector.Fields(context.Background(), Target{Name: "logs", Kind: "alias"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fields.Fields) != 2 || !fields.Fields[0].Conflicting || fields.Fields[1].Name != "service.name" {
		t.Fatalf("fields = %#v", fields)
	}
}

func TestInspectorRejectsInvalidTarget(t *testing.T) {
	client, _ := opensearch.NewClient(opensearch.Config{Addresses: []string{"http://127.0.0.1:1"}})
	inspector, _ := New(client, Options{})
	if _, err := inspector.Fields(context.Background(), Target{Name: "*", Kind: "wildcard"}); err == nil {
		t.Fatal("invalid target must fail before making a request")
	}
}
