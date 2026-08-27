package connections

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/entity"
)

func (options InspectFlags) Filters() []entity.Filter[InspectFlags] {
	return []entity.Filter[InspectFlags]{
		inspectDatabaseFilter{service: options.service},
		inspectTargetFilter{},
	}
}

func (options *InspectFlags) SetClickyActionContext(ctx context.Context, id string) {
	options.context = ctx
	options.connectionID = id
}

type inspectDatabaseFilter struct {
	service *Service
}

func (inspectDatabaseFilter) Key() string   { return "database" }
func (inspectDatabaseFilter) Label() string { return "Database" }

func (filter inspectDatabaseFilter) Lookup(options *InspectFlags) (map[string]api.Textable, error) {
	if filter.service == nil {
		return nil, fmt.Errorf("connection inspection lookup requires a service")
	}
	if options.context == nil {
		return nil, fmt.Errorf("connection inspection lookup requires a request context")
	}
	database, err := filter.service.database()
	if err != nil {
		return nil, err
	}
	raw, connection, err := filter.service.resolveConnection(resolveConnectionOptions{
		Context: options.context, Database: database, ID: options.connectionID, RefreshSecrets: options.Refresh,
	})
	if err != nil {
		return nil, err
	}
	requestContext, cancel := inspectionContext(options.context, connection.Type, 15*time.Second)
	defer cancel()
	inspected, err := filter.service.browser.inspectConnection(requestContext, connection, options.Database, "", "", options.Refresh)
	if err != nil {
		return nil, fmt.Errorf("inspect connection %q: %s", connection.Name, sanitizeConnectionError(err, raw, connection))
	}
	options.inspected = &inspected
	if options.Database == "" {
		return nil, nil
	}
	if inspected.Kind != "sql" {
		return nil, fmt.Errorf("database %q is only valid for SQL connections", options.Database)
	}
	if !inspectionHasDatabase(inspected, options.Database) {
		return nil, fmt.Errorf("database %q was not discovered", options.Database)
	}
	return map[string]api.Textable{options.Database: api.Text{Content: options.Database}}, nil
}

func (inspectDatabaseFilter) Options(options InspectFlags) map[string]api.Textable {
	if options.inspected == nil || options.inspected.Kind != "sql" {
		return nil
	}
	values := map[string]api.Textable{}
	for _, database := range options.inspected.Databases {
		values[database] = api.Text{Content: database}
	}
	if options.inspected.Database != "" {
		values[options.inspected.Database] = api.Text{Content: options.inspected.Database}
	}
	return values
}

func inspectionHasDatabase(inspected browserInspection, database string) bool {
	if inspected.Database == database {
		return true
	}
	for _, candidate := range inspected.Databases {
		if candidate == database {
			return true
		}
	}
	return false
}

type inspectTargetFilter struct{}

func (inspectTargetFilter) Key() string   { return "target" }
func (inspectTargetFilter) Label() string { return "Target" }

func (inspectTargetFilter) Lookup(options *InspectFlags) (map[string]api.Textable, error) {
	if options.inspected == nil {
		return nil, fmt.Errorf("target lookup requires connection inspection metadata")
	}
	if options.Target == "" {
		return nil, nil
	}
	targets := connectionInspectionTargets(options.connectionID, *options, *options.inspected)
	matches := matchingInspectionTargets(*options, targets)
	if len(matches) == 0 && options.inspected.Kind == "opensearch" && strings.Contains(options.Target, "*") {
		options.TargetKind = "pattern"
		return map[string]api.Textable{options.Target: api.Text{Content: options.Target}}, nil
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("inspection target %q matched %d discovered targets", options.Target, len(matches))
	}
	selected := matches[0]
	options.Schema = selected.Schema
	options.Target = selected.Target
	options.TargetKind = selected.Kind
	value := inspectionTargetLookupValue(*options.inspected, selected)
	return map[string]api.Textable{value: api.Text{Content: selected.Name}}, nil
}

func (inspectTargetFilter) Options(options InspectFlags) map[string]api.Textable {
	if options.inspected == nil {
		return nil
	}
	values := map[string]api.Textable{}
	for _, target := range connectionInspectionTargets(options.connectionID, options, *options.inspected) {
		values[inspectionTargetLookupValue(*options.inspected, target)] = api.Text{Content: target.Name}
	}
	return values
}

func matchingInspectionTargets(options InspectFlags, targets []ConnectionInspectionTarget) []ConnectionInspectionTarget {
	var matches []ConnectionInspectionTarget
	for _, target := range targets {
		if options.inspected.Kind == "sql" {
			if options.Target == target.Name || options.Target == target.Target && (options.Schema == "" || options.Schema == target.Schema) {
				matches = append(matches, target)
			}
			continue
		}
		if options.Target == target.Target && (options.TargetKind == "" || options.TargetKind == target.Kind) {
			matches = append(matches, target)
		}
	}
	return matches
}

func inspectionTargetLookupValue(inspected browserInspection, target ConnectionInspectionTarget) string {
	if inspected.Kind == "sql" {
		return target.Name
	}
	return target.Target
}
