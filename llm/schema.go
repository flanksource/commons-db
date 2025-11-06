package llm

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// JSONSchema represents a JSON Schema definition
type JSONSchema struct {
	Type                 string                `json:"type,omitempty"`
	Properties           map[string]JSONSchema `json:"properties,omitempty"`
	Required             []string              `json:"required,omitempty"`
	Items                *JSONSchema           `json:"items,omitempty"`
	Description          string                `json:"description,omitempty"`
	Enum                 []interface{}         `json:"enum,omitempty"`
	AdditionalProperties bool                  `json:"additionalProperties,omitempty"`
}

// generateJSONSchema generates a JSON Schema from a Go struct type
func generateJSONSchema(v interface{}) (*JSONSchema, error) {
	t := reflect.TypeOf(v)
	if t == nil {
		return nil, fmt.Errorf("cannot generate schema from nil")
	}

	// Dereference pointer types
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("schema generation requires a struct type, got %s", t.Kind())
	}

	return buildSchema(t), nil
}

// buildSchema recursively builds a JSON Schema from a reflect.Type
func buildSchema(t reflect.Type) *JSONSchema {
	// Dereference pointer types
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	schema := &JSONSchema{}

	switch t.Kind() {
	case reflect.Struct:
		schema.Type = "object"
		schema.Properties = make(map[string]JSONSchema)
		var required []string

		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)

			// Skip unexported fields
			if !field.IsExported() {
				continue
			}

			// Get JSON tag
			jsonTag := field.Tag.Get("json")
			if jsonTag == "-" {
				continue
			}

			fieldName := field.Name
			isRequired := true

			// Parse json tag
			if jsonTag != "" {
				parts := strings.Split(jsonTag, ",")
				if parts[0] != "" {
					fieldName = parts[0]
				}
				for _, part := range parts[1:] {
					if part == "omitempty" {
						isRequired = false
					}
				}
			}

			// Build field schema
			fieldSchema := buildSchema(field.Type)

			// Add description from comment if available
			if desc := field.Tag.Get("description"); desc != "" {
				fieldSchema.Description = desc
			}

			schema.Properties[fieldName] = *fieldSchema

			if isRequired {
				required = append(required, fieldName)
			}
		}

		if len(required) > 0 {
			schema.Required = required
		}

	case reflect.Slice, reflect.Array:
		schema.Type = "array"
		itemSchema := buildSchema(t.Elem())
		schema.Items = itemSchema

	case reflect.Map:
		schema.Type = "object"
		schema.AdditionalProperties = true

	case reflect.String:
		schema.Type = "string"

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		schema.Type = "integer"

	case reflect.Float32, reflect.Float64:
		schema.Type = "number"

	case reflect.Bool:
		schema.Type = "boolean"

	case reflect.Interface:
		// For interface{}, allow any type
		return &JSONSchema{}

	default:
		schema.Type = "string"
	}

	return schema
}

// toJSONSchemaString converts a JSONSchema to its JSON string representation
func toJSONSchemaString(schema *JSONSchema) (string, error) {
	data, err := json.Marshal(schema)
	if err != nil {
		return "", fmt.Errorf("failed to marshal schema: %w", err)
	}
	return string(data), nil
}
