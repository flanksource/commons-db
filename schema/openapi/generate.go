package openapi

import (
	"fmt"
	"os"

	"github.com/invopop/jsonschema"
)

func GenerateSchema(obj any) ([]byte, error) {
	return jsonschema.Reflect(obj).MarshalJSON()
}

func WriteSchemaToFile(path string, obj any) error {
	data, err := GenerateSchema(obj)
	if err != nil {
		return fmt.Errorf("error generating json schema: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("unabled to write schema to path[%s]: %w", path, err)
	}

	return nil
}
