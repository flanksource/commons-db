package schema

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
		for _, raw := range fields {
			if field, ok := raw.(Schema); ok {
				field["x-col-span"] = "full"
			}
		}
	}
	fields["kubeconfig"] = Schema{"type": "string", "title": "Kubeconfig file", "description": "Optional path on the server. Leave empty to use normal Kubernetes configuration discovery.", "x-clicky-component": "k8s-kubeconfig-selector", "x-clicky-order": -2}
	fields["kubecontext"] = Schema{"type": "string", "title": "Kubecontext", "description": "Optional context in the selected kubeconfig. Leave empty to use its current context.", "x-clicky-component": "k8s-context-selector", "x-clicky-order": -1}
}
