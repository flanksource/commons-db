package schema

import (
	"strings"

	"github.com/flanksource/commons-db/query"
)

// processorSpec describes one post-query step. Both pickers are driven by the
// live registries, so a processor or library preset that is registered but not
// listed here cannot happen. Empty enums are omitted because they admit no value.
func processorSpec() Schema {
	use := Schema{
		"type": "string", "title": "Library processor", "x-clicky-order": 0,
		"description": "Reusable preset supplying the type and its configuration; anything set below is merged over it",
	}
	if names := query.NamedProcessorNames(); len(names) > 0 {
		use["enum"] = names
		use["x-enum-labels"] = namedProcessorLabels()
		use["x-enum-display"] = "combobox"
		use["description"] = use["description"].(string) + ".\n\n" + namedProcessorHelp()
	}
	typ := Schema{
		"type": "string", "title": "Type", "x-clicky-order": 1,
		"description": "Registered processor key; leave blank when a library processor is chosen",
	}
	if types := query.RegisteredProcessors(); len(types) > 0 {
		typ["enum"] = types
	}
	return Schema{
		"type": "object", "title": "Processor",
		"properties": Schema{
			"use": use, "type": typ,
			"config": Schema{"type": "object", "title": "Config", "x-clicky-order": 2, "description": "Processor-specific configuration"},
		},
	}
}

func namedProcessorPresets() Schema {
	presets := Schema{}
	for _, named := range query.NamedProcessors() {
		presets[named.Name] = Schema{
			"type": named.Spec.Type, "title": named.Title,
			"description": named.Description, "config": named.Spec.Config,
		}
	}
	return presets
}

func namedProcessorLabels() map[string]string {
	labels := map[string]string{}
	for _, entry := range query.NamedProcessors() {
		if entry.Title != "" {
			labels[entry.Name] = entry.Title
		}
	}
	return labels
}

func namedProcessorHelp() string {
	var help []string
	for _, entry := range query.NamedProcessors() {
		if entry.Description != "" {
			help = append(help, "- `"+entry.Name+"`: "+entry.Description)
		}
	}
	return strings.Join(help, "\n")
}
