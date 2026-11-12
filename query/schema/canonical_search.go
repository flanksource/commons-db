package schema

func canonicalSearchSchema() Schema {
	result := searchSpecProp()
	result["title"] = "Logical span conditions"
	result["description"] = "Canonical span fields and normalized attributes. Resolved to the selected connection's index layout at execution; mutually exclusive with native queries."
	delete(result["properties"].(Schema), "timeFieldFormat")
	delete(result["properties"].(Schema), "aggregations")
	return result
}
