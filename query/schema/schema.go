// Package schema generates JSON Schema (Draft 2020-12) documents that drive the
// clicky-ui forms and tables of the query app:
//
//   - Connection: a polymorphic if/then schema keyed on the connection `type`,
//     so the connection form shows the right fields per backend.
//   - Profile: the profile-setup schema for creating/editing a Profile.
//   - ProfileInstance: a per-profile schema whose `properties` drive the FilterBar
//     and whose `x-clicky-columns` drive the DataTable.
//
// Schemas are assembled as plain maps (the same approach as the legacy schema
// emit) so the conditional envelopes stay explicit and dependency-free.
package schema

import "encoding/json"

// Draft is the JSON Schema dialect emitted by this package.
const Draft = "https://json-schema.org/draft/2020-12/schema"

// Schema is a JSON Schema document or subschema.
type Schema = map[string]any

// JSON renders a schema as indented JSON.
func JSON(s Schema) ([]byte, error) {
	return json.MarshalIndent(s, "", "  ")
}

// strProp builds a string property with an optional title/description.
func strProp(title, description string) Schema {
	s := Schema{"type": "string"}
	if title != "" {
		s["title"] = title
	}
	if description != "" {
		s["description"] = description
	}
	return s
}

// labelOr returns label when set, otherwise name.
func labelOr(label, name string) string {
	if label != "" {
		return label
	}
	return name
}
