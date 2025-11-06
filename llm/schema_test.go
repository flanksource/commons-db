package llm

import (
	"encoding/json"
	"testing"
)

func TestGenerateJSONSchema(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected JSONSchema
		wantErr  bool
	}{
		{
			name: "simple struct",
			input: struct {
				Name string `json:"name"`
				Age  int    `json:"age"`
			}{},
			expected: JSONSchema{
				Type: "object",
				Properties: map[string]JSONSchema{
					"name": {Type: "string"},
					"age":  {Type: "integer"},
				},
				Required: []string{"name", "age"},
			},
		},
		{
			name: "struct with optional field",
			input: struct {
				Name  string  `json:"name"`
				Email *string `json:"email,omitempty"`
			}{},
			expected: JSONSchema{
				Type: "object",
				Properties: map[string]JSONSchema{
					"name":  {Type: "string"},
					"email": {Type: "string"},
				},
				Required: []string{"name"},
			},
		},
		{
			name: "struct with array",
			input: struct {
				Tags []string `json:"tags"`
			}{},
			expected: JSONSchema{
				Type: "object",
				Properties: map[string]JSONSchema{
					"tags": {
						Type:  "array",
						Items: &JSONSchema{Type: "string"},
					},
				},
				Required: []string{"tags"},
			},
		},
		{
			name: "nested struct",
			input: struct {
				User struct {
					Name string `json:"name"`
				} `json:"user"`
			}{},
			expected: JSONSchema{
				Type: "object",
				Properties: map[string]JSONSchema{
					"user": {
						Type: "object",
						Properties: map[string]JSONSchema{
							"name": {Type: "string"},
						},
						Required: []string{"name"},
					},
				},
				Required: []string{"user"},
			},
		},
		{
			name: "struct with description tags",
			input: struct {
				Name string `json:"name" description:"User's full name"`
				Age  int    `json:"age" description:"User's age in years"`
			}{},
			expected: JSONSchema{
				Type: "object",
				Properties: map[string]JSONSchema{
					"name": {Type: "string", Description: "User's full name"},
					"age":  {Type: "integer", Description: "User's age in years"},
				},
				Required: []string{"name", "age"},
			},
		},
		{
			name: "pointer to struct",
			input: &struct {
				Name string `json:"name"`
			}{},
			expected: JSONSchema{
				Type: "object",
				Properties: map[string]JSONSchema{
					"name": {Type: "string"},
				},
				Required: []string{"name"},
			},
		},
		{
			name:    "non-struct type",
			input:   "string",
			wantErr: true,
		},
		{
			name:    "nil input",
			input:   nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := generateJSONSchema(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("generateJSONSchema() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}

			assertSchemaEqual(t, tt.expected, *result)
		})
	}
}

func TestToJSONSchemaString(t *testing.T) {
	schema := &JSONSchema{
		Type: "object",
		Properties: map[string]JSONSchema{
			"name": {Type: "string"},
			"age":  {Type: "integer"},
		},
		Required: []string{"name", "age"},
	}

	result, err := toJSONSchemaString(schema)
	if err != nil {
		t.Fatalf("toJSONSchemaString() error = %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result as JSON: %v", err)
	}

	if parsed["type"] != "object" {
		t.Errorf("expected type=object, got %v", parsed["type"])
	}

	properties, ok := parsed["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties is not a map")
	}

	if len(properties) != 2 {
		t.Errorf("expected 2 properties, got %d", len(properties))
	}
}

func assertSchemaEqual(t *testing.T, expected, actual JSONSchema) {
	t.Helper()

	if expected.Type != actual.Type {
		t.Errorf("Type: expected %q, got %q", expected.Type, actual.Type)
	}

	if expected.Description != actual.Description {
		t.Errorf("Description: expected %q, got %q", expected.Description, actual.Description)
	}

	if len(expected.Required) != len(actual.Required) {
		t.Errorf("Required length: expected %d, got %d", len(expected.Required), len(actual.Required))
	}

	if len(expected.Properties) != len(actual.Properties) {
		t.Errorf("Properties length: expected %d, got %d", len(expected.Properties), len(actual.Properties))
	}

	for name, expectedProp := range expected.Properties {
		actualProp, ok := actual.Properties[name]
		if !ok {
			t.Errorf("Property %q missing in actual schema", name)
			continue
		}
		assertSchemaEqual(t, expectedProp, actualProp)
	}

	if expected.Items != nil && actual.Items != nil {
		assertSchemaEqual(t, *expected.Items, *actual.Items)
	} else if (expected.Items == nil) != (actual.Items == nil) {
		t.Errorf("Items: expected nil=%v, got nil=%v", expected.Items == nil, actual.Items == nil)
	}
}
