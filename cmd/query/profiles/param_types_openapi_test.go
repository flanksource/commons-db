package profiles

import (
	"testing"

	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/commons-db/query"
)

func TestProfileOpenAPIAdvertisesDateTimeAndDurationParameterSchemas(t *testing.T) {
	spec := &rpc.OpenAPISpec{Paths: map[string]rpc.OpenAPIPath{}, Clicky: &rpc.ClickySpecMeta{}}
	if err := addProfileToSpec(spec, query.Profile{
		Name: "temporal",
		Params: []query.ParamDef{
			{Name: "day", Type: query.ParamTypeDate},
			{Name: "started_at", Type: query.ParamTypeDateTime},
			{Name: "window", Type: query.ParamTypeDuration},
		},
	}); err != nil {
		t.Fatal(err)
	}

	parameters := spec.Paths["/api/v1/profile/profile-temporal"]["get"].Parameters
	want := map[string]struct {
		typeName string
		format   string
	}{
		"day":        {typeName: "string", format: "date"},
		"started_at": {typeName: "string", format: "date-time"},
		"window":     {typeName: "string"},
	}
	for _, parameter := range parameters {
		expected, ok := want[parameter.Name]
		if !ok {
			continue
		}
		if parameter.Schema.Type != expected.typeName || parameter.Schema.Format != expected.format {
			t.Fatalf("parameter %q schema = %#v, want type %q format %q", parameter.Name, parameter.Schema, expected.typeName, expected.format)
		}
		delete(want, parameter.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing parameter schemas: %v", want)
	}
}
