package schema

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/flanksource/commons-db/models"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ValidateConnection checks the same provider-specific contract used by the form.
func ValidateConnection(connection models.Connection) error {
	if slices.Contains([]string{models.ConnectionTypeHTTP, models.ConnectionTypeOpenSearch, models.ConnectionTypeElasticSearch, models.ConnectionTypePrometheus, models.ConnectionTypeLoki, models.ConnectionTypeJaeger}, connection.Type) {
		connection.Properties = maps.Clone(connection.Properties)
		if connection.Properties == nil {
			connection.Properties = map[string]string{}
		}
		if connection.Properties["authType"] == "" {
			connection.Properties["authType"] = "none"
			if connection.Username != "" || connection.Password != "" {
				connection.Properties["authType"] = "basic"
			}
		}
		connection.Properties["username"] = connection.Username
		connection.Properties["password"] = connection.Password
	}
	compiler := jsonschema.NewCompiler()
	const location = "https://schemas.example.com/connection.json"
	document, err := json.Marshal(Connection(ConnectionOptions{}))
	if err != nil {
		return err
	}
	var schema any
	if err := json.Unmarshal(document, &schema); err != nil {
		return err
	}
	if err := compiler.AddResource(location, schema); err != nil {
		return err
	}
	compiled, err := compiler.Compile(location)
	if err != nil {
		return fmt.Errorf("compile connection schema: %w", err)
	}
	data, err := json.Marshal(connection)
	if err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if err := compiled.Validate(value); err != nil {
		return fmt.Errorf("invalid connection configuration: %w", err)
	}
	return nil
}
