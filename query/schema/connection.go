package schema

// Connection returns the JSON Schema for models.Connection. The base properties
// are always present; an `allOf` of if/then branches keyed on `type` narrows the
// relevant fields (and marks per-type requirements) for each backend, so the
// connection form adapts to the selected type.
func Connection() Schema {
	base := Schema{
		"name":         Schema{"type": "string", "title": "Name"},
		"namespace":    Schema{"type": "string", "title": "Namespace"},
		"type":         Schema{"type": "string", "title": "Type", "enum": connectionTypeEnum()},
		"url":          Schema{"type": "string", "title": "URL"},
		"username":     Schema{"type": "string", "title": "Username"},
		"password":     Schema{"type": "string", "title": "Password", "format": "password"},
		"certificate":  Schema{"type": "string", "title": "Certificate"},
		"insecure_tls": Schema{"type": "boolean", "title": "Insecure TLS"},
		"properties": Schema{
			"type":                 "object",
			"title":                "Properties",
			"additionalProperties": Schema{"type": "string"},
		},
	}

	allOf := make([]any, 0, len(connectionTypes))
	for _, spec := range connectionTypes {
		allOf = append(allOf, connectionBranch(spec))
	}

	return Schema{
		"$schema":    Draft,
		"title":      "Connection",
		"type":       "object",
		"required":   []string{"name", "type"},
		"properties": base,
		"allOf":      allOf,
	}
}

// connectionBranch builds one `{if: type==X, then: {...}}` conditional for a
// connection type, narrowing base fields and (for property-backed fields) the
// nested `properties` object.
func connectionBranch(spec connTypeSpec) Schema {
	props := Schema{}
	var required []string
	propProps := Schema{}
	var propRequired []string

	for _, f := range spec.Fields {
		fs := fieldSchema(f)
		if f.Property != "" {
			propProps[f.Property] = fs
			if f.Required {
				propRequired = append(propRequired, f.Property)
			}
			continue
		}
		props[f.Base] = fs
		if f.Required {
			required = append(required, f.Base)
		}
	}

	if len(propProps) > 0 {
		propsObj := Schema{"type": "object", "title": "Properties", "properties": propProps}
		if len(propRequired) > 0 {
			propsObj["required"] = propRequired
		}
		props["properties"] = propsObj
	}

	then := Schema{}
	if len(props) > 0 {
		then["properties"] = props
	}
	if len(required) > 0 {
		then["required"] = required
	}

	return Schema{
		"if": Schema{
			"properties": Schema{"type": Schema{"const": spec.Type}},
			"required":   []string{"type"},
		},
		"then": then,
	}
}

// fieldSchema converts a connField to its property subschema.
func fieldSchema(f connField) Schema {
	typ := f.Type
	if typ == "" {
		typ = "string"
	}
	s := Schema{"type": typ}
	if f.Label != "" {
		s["title"] = f.Label
	}
	if f.Description != "" {
		s["description"] = f.Description
	}
	if f.Format != "" {
		s["format"] = f.Format
	}
	if len(f.Enum) > 0 {
		s["enum"] = f.Enum
	}
	return s
}
