package query

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/flanksource/clicky/api"
)

// prettyTagKeys are the key=value entries api.ParsePrettyTagWithName gives a
// meaning to, plus the three profile keys it carries through as format
// options. Anything else would land silently in FormatOptions, which is how a
// typo becomes a column that quietly renders the default way.
var prettyTagKeys = []string{
	"label", "title", "sort", "dir", "direction", "format", "digits", "style",
	"label_style", "header_style", "row_style", "indent", "render", "max_depth",
	api.ColorGreen, api.ColorRed, api.ColorBlue, "yellow", "cyan", "magenta",
	"type", "kind", "unit",
}

// prettyTagFlags are the bare entries api.ParsePrettyTagWithName recognises.
var prettyTagFlags = []string{
	"title", "table", "tree", "struct", api.FormatHide, api.SortAsc, api.SortDesc,
	"compact", "short", "no_icons", "ascii",
}

// nestedRenderFormats describe how clicky draws a nested value in a terminal.
// A column holding one is typed json, so they have nothing to say to it.
var nestedRenderFormats = []string{api.FormatTable, api.FormatTree, "struct"}

func applyPrettyTag(column *ColumnDef, tag string) error {
	if strings.TrimSpace(tag) == "-" {
		column.Hidden = true
		return nil
	}
	if err := validatePrettyTag(tag); err != nil {
		return err
	}
	pretty := api.ParsePrettyTagWithName(column.Name, tag)
	if pretty.FormatOptions["label_set"] == "true" {
		column.Label = pretty.Label
	}
	switch {
	case pretty.Format == api.FormatHide:
		column.Hidden = true
	case pretty.Format != "" && !slices.Contains(nestedRenderFormats, pretty.Format):
		column.Format = pretty.Format
	}
	if value, ok := pretty.FormatOptions["type"]; ok {
		if !slices.Contains(ColumnTypeValues(), value) {
			return fmt.Errorf("pretty type %q is not one of %s", value, strings.Join(ColumnTypeValues(), ", "))
		}
		column.Type = ColumnType(value)
	}
	if value, ok := pretty.FormatOptions["kind"]; ok {
		kind := ColumnKind(value)
		if kind != ColumnKindTimestamp && kind != ColumnKindTags && kind != ColumnKindStatus {
			return fmt.Errorf("pretty kind %q is not one of %s, %s, %s", value, ColumnKindTimestamp, ColumnKindTags, ColumnKindStatus)
		}
		column.Kind = kind
	}
	column.Unit = pretty.FormatOptions["unit"]
	return nil
}

func validatePrettyTag(tag string) error {
	for _, part := range strings.Split(tag, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, _, hasValue := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		if hasValue && !slices.Contains(prettyTagKeys, key) {
			return fmt.Errorf("pretty key %q is not one clicky or a profile column reads", key)
		}
		if !hasValue && !slices.Contains(prettyTagFlags, key) {
			return fmt.Errorf("pretty flag %q is not one clicky or a profile column reads", key)
		}
	}
	return nil
}

// applySortTag checks clicky's sort key against the one thing a profile can
// publish: every addressable column is sortable under its own name, so a key
// naming anything else is a sort that could never be requested.
func applySortTag(column ColumnDef, tag reflect.StructTag) error {
	key, declared := tag.Lookup("sort")
	if !declared {
		return nil
	}
	key = strings.TrimSpace(key)
	if key != column.Name {
		return fmt.Errorf("sort key %q must be the column name %q; a profile orders by column name", key, column.Name)
	}
	if column.Hidden {
		return fmt.Errorf("sort key %q is declared on a hidden column, which a profile never offers to sort by", key)
	}
	return nil
}

// parseFilterTag reads the filter tag in the pretty tag's grammar: comma
// separated, key=value or a bare flag.
func parseFilterTag(tag reflect.StructTag) (*ColumnFilterDef, error) {
	raw, declared := tag.Lookup("filter")
	if !declared {
		return nil, nil
	}
	if strings.TrimSpace(raw) == "-" {
		return &ColumnFilterDef{Disabled: true}, nil
	}
	def := &ColumnFilterDef{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, hasValue := strings.Cut(part, "=")
		var err error
		if hasValue {
			err = applyFilterTagValue(def, strings.TrimSpace(key), strings.TrimSpace(value))
		} else {
			err = applyFilterTagFlag(def, key)
		}
		if err != nil {
			return nil, err
		}
	}
	return def, nil
}

func applyFilterTagFlag(def *ColumnFilterDef, flag string) error {
	if flag == "disabled" {
		def.Disabled = true
		return nil
	}
	if !slices.Contains(ColumnFilterKindValues(), flag) {
		return fmt.Errorf("filter flag %q is neither disabled nor a kind (%s)", flag, strings.Join(ColumnFilterKindValues(), ", "))
	}
	if def.Kind != "" {
		return fmt.Errorf("filter declares two filter kinds, %q and %q", def.Kind, flag)
	}
	def.Kind = ColumnFilterKind(flag)
	return nil
}

func applyFilterTagValue(def *ColumnFilterDef, key, value string) error {
	switch key {
	case "field":
		def.Field = value
	case "options":
		def.Options = strings.Split(value, "|")
		for index := range def.Options {
			def.Options[index] = strings.TrimSpace(def.Options[index])
		}
	case "limit":
		limit, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("filter limit %q is not a number", value)
		}
		def.Limit = &limit
	case "lookup", "multi":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("filter %s %q is not a boolean", key, value)
		}
		if key == "lookup" {
			def.Lookup = &parsed
		} else {
			def.Multi = &parsed
		}
	default:
		return fmt.Errorf("filter key %q is not one of field, options, limit, lookup, multi", key)
	}
	return nil
}
