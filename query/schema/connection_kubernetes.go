// Adds the Kubernetes selection to a connection form: the kubeconfig file and context its
// cluster-backed values (secret://, configmap://, …) are read from.
package schema

// addKubernetesSelection adds the kubeconfig and kubecontext fields to the
// form props, beside the type's own properties.
func addKubernetesSelection(props Schema) {
	properties, ok := props["properties"].(Schema)
	if !ok {
		properties = Schema{"type": "object", "title": "Properties"}
		props["properties"] = properties
	}
	fields, ok := properties["properties"].(Schema)
	if !ok {
		fields = Schema{}
		properties["properties"] = fields
	}
	if _, ordered := properties["x-clicky-order"]; !ordered {
		properties["x-clicky-order"] = 7
	}
	if _, hasColumns := properties["x-columns"]; !hasColumns {
		properties["x-columns"] = 2
		spanFullRows(fields)
		// Conditional fields (an HTTP type's authentication credentials) render
		// in the same grid, so they need their own rows too.
		branches, _ := properties["allOf"].([]any)
		for _, branch := range branches {
			then, _ := branch.(Schema)["then"].(Schema)
			if branchFields, ok := then["properties"].(Schema); ok {
				spanFullRows(branchFields)
			}
		}
	}
	fields["kubeconfig"] = Schema{"type": "string", "title": "Kubeconfig file", "description": "Optional path on the server. Leave empty to use normal Kubernetes configuration discovery.", "x-clicky-component": "k8s-kubeconfig-selector", "x-clicky-order": -2}
	fields["kubecontext"] = Schema{"type": "string", "title": "Kubecontext", "description": "Optional context in the selected kubeconfig. Leave empty to use its current context.", "x-clicky-component": "k8s-context-selector", "x-clicky-order": -1}
}

// spanFullRows gives each field its own row of the two-column grid the
// Kubernetes selectors share.
func spanFullRows(fields Schema) {
	for _, raw := range fields {
		if field, ok := raw.(Schema); ok {
			field["x-col-span"] = "full"
		}
	}
}
