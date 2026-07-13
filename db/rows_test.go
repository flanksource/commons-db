package db

import (
	"reflect"
	"testing"
)

func TestNormalizeSQLValue(t *testing.T) {
	tests := []struct {
		name         string
		databaseType string
		value        any
		want         any
	}{
		{
			name:         "json object",
			databaseType: "JSONB",
			value:        []byte(`{"name":"query","nested":{"enabled":true}}`),
			want: map[string]any{
				"name": "query",
				"nested": map[string]any{
					"enabled": true,
				},
			},
		},
		{
			name:         "json array",
			databaseType: "JSON",
			value:        []byte(`[1,{"enabled":true},null]`),
			want:         []any{float64(1), map[string]any{"enabled": true}, nil},
		},
		{
			name:         "integer array",
			databaseType: "_INT4",
			value:        "{1,2,NULL}",
			want:         []any{int32(1), int32(2), nil},
		},
		{
			name:         "quoted text array",
			databaseType: "_TEXT",
			value:        `{plain,"comma,value","NULL",NULL}`,
			want:         []any{"plain", "comma,value", "NULL", nil},
		},
		{
			name:         "multidimensional array",
			databaseType: "_INT4",
			value:        "{{1,2},{3,4}}",
			want: []any{
				[]any{int32(1), int32(2)},
				[]any{int32(3), int32(4)},
			},
		},
		{
			name:         "empty array",
			databaseType: "_TEXT",
			value:        "{}",
			want:         []any{},
		},
		{
			name:         "ordinary bytes remain bytes",
			databaseType: "BYTEA",
			value:        []byte{1, 2, 3},
			want:         []byte{1, 2, 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeSQLValue(tt.databaseType, tt.value)
			if err != nil {
				t.Fatalf("normalizeSQLValue() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("normalizeSQLValue() = %#v (%T), want %#v (%T)", got, got, tt.want, tt.want)
			}
		})
	}
}
