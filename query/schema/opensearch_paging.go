package schema

func addOpenSearchPagingOptions(props Schema) {
	props["pagingMode"] = Schema{"type": "string", "title": "Paging mode", "enum": []string{"scroll", "pit"}, "description": "Override the connection's paging mode for reads above the paging threshold. Leave unset to inherit."}
	props["pagingThreshold"] = Schema{"type": "integer", "title": "Paging threshold", "minimum": 0, "maximum": 10000, "description": "Override the connection's requested-row threshold (default 10000). Zero always uses scroll/PIT. Leave unset to inherit."}
}
