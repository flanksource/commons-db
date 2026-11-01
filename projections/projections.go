// Package projections maps JSON source rows into YAML documents using CEL and JSONPath.
package projections

import (
	"fmt"
	"maps"
	"path/filepath"
	"time"

	"github.com/flanksource/commons-db/connection/store"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
)

type LoadOptions struct {
	Paths []string
	Name  string
}

type Options struct {
	Context map[string]any
	DryRun  bool
}

type ProjectionRunResult struct {
	Projection string              `json:"projection" yaml:"projection"`
	Context    map[string]any      `json:"context" yaml:"context"`
	Items      []map[string]any    `json:"items" yaml:"items"`
	Filtered   int                 `json:"filtered" yaml:"filtered"`
	Warnings   []ProjectionWarning `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}

type ProjectionVerifyResult struct {
	Files       []string `json:"files" yaml:"files"`
	Projections []string `json:"projections" yaml:"projections"`
}

// Load reads files and directories in order, enforcing unique names before selection.
func Load(options LoadOptions) ([]Projection, error) {
	if len(options.Paths) == 0 {
		return nil, fmt.Errorf("a Projection file or directory is required, as an argument or --file")
	}
	projections, err := loadProjectionPaths(options.Paths)
	if err != nil {
		return nil, err
	}
	if err := validateProjectionNames(projections); err != nil {
		return nil, err
	}
	return selectProjections(projections, options.Name)
}

// Verify checks manifests, targets and schemas without executing sources or resolving credentials.
func Verify(projections []Projection) (ProjectionVerifyResult, error) {
	result := ProjectionVerifyResult{Files: []string{}, Projections: []string{}}
	if err := preflightProjections(projections); err != nil {
		return result, err
	}
	seen := map[string]bool{}
	for _, p := range projections {
		if err := verifyProjection(p); err != nil {
			return result, fmt.Errorf("projection %s: %w", p.Metadata.Name, err)
		}
		if !seen[p.manifest] {
			result.Files = append(result.Files, p.manifest)
			seen[p.manifest] = true
		}
		result.Projections = append(result.Projections, p.Metadata.Name)
	}
	return result, nil
}

// Run fetches and filters source rows, leaving targets untouched.
func Run(ctx context.Context, projections []Projection, options Options) ([]ProjectionRunResult, error) {
	if err := preflightProjections(projections); err != nil {
		return nil, err
	}
	ctx, err := localConnections(ctx)
	if err != nil {
		return nil, err
	}
	values := projectionContext(options.Context)
	results := make([]ProjectionRunResult, 0, len(projections))
	for _, p := range projections {
		source, err := queryProjection(ctx, p, values)
		if err != nil {
			return results, fmt.Errorf("projection %s: %w", p.Metadata.Name, err)
		}
		items, filtered, err := selectProjectionSources(p, source)
		if err != nil {
			return results, err
		}
		results = append(results, ProjectionRunResult{Projection: p.Metadata.Name, Context: values, Items: items, Filtered: filtered, Warnings: source.Warnings})
	}
	return results, nil
}

// Apply runs projections sequentially; an individual failure does not hide later results.
func Apply(ctx context.Context, projections []Projection, options Options) ([]ProjectionApplyResult, error) {
	if err := preflightProjections(projections); err != nil {
		return nil, err
	}
	ctx, err := localConnections(ctx)
	if err != nil {
		return nil, err
	}
	values := projectionContext(options.Context)
	results := make([]ProjectionApplyResult, 0, len(projections))
	failed, applicable := 0, 0
	for _, p := range projections {
		result := ProjectionApplyResult{Projection: p.Metadata.Name, Status: projectionSkipped, DryRun: options.DryRun}
		if p.Spec.Target != nil {
			applicable++
			source, applyErr := queryProjection(ctx, p, values)
			if applyErr == nil {
				result, applyErr = applyProjection(p, source, options.DryRun)
			}
			result.Status = projectionApplied
			if len(result.Warnings) > 0 {
				result.Status = projectionWarned
			}
			if applyErr != nil {
				result.Status, result.Error = projectionFailed, applyErr.Error()
				failed++
			}
		}
		results = append(results, result)
	}
	if applicable == 0 {
		return results, fmt.Errorf("no selected Projection document has a target")
	}
	if failed > 0 {
		return results, fmt.Errorf("%d of %d projections failed", failed, len(results))
	}
	return results, nil
}

func preflightProjections(projections []Projection) error {
	if len(projections) == 0 {
		return fmt.Errorf("at least one Projection is required")
	}
	if err := validateProjectionNames(projections); err != nil {
		return err
	}
	for _, p := range projections {
		if err := p.validate(); err != nil {
			return err
		}
		if _, err := compileProjection(p); err != nil {
			return fmt.Errorf("projection %s: %w", p.Metadata.Name, err)
		}
	}
	return nil
}

func queryProjection(ctx context.Context, p Projection, values map[string]any) (projectionSourceResult, error) {
	result := projectionSourceResult{Context: values}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	var err error
	if p.Spec.Source.Query.Exec != nil {
		result.Items, err = queryExecProjection(ctx, p, values)
	} else {
		result.Items, result.Warnings, err = queryHTTPProjection(ctx, p, values)
	}
	return result, err
}

func projectionContext(values map[string]any) map[string]any {
	result := maps.Clone(values)
	if result == nil {
		result = map[string]any{}
	}
	now := time.Now().UTC()
	result["observed_at"], result["date"] = now.Format(time.RFC3339), now.Format("2006-01-02")
	if _, exists := result["name"]; !exists {
		result["name"] = ""
	}
	return result
}

func localConnections(ctx context.Context) (context.Context, error) {
	dir, err := store.ConfigDir(store.GlobalApp)
	if err != nil {
		return ctx, err
	}
	connections, err := store.New(store.Layer{Name: store.LayerGlobal, Dir: filepath.Join(dir, "connections")})
	if err != nil {
		return ctx, err
	}
	return ctx.WithConnectionResolver(func(ctx context.Context, reference string) (*models.Connection, error) {
		conn, err := connections.ResolveConnection(ctx, reference)
		if err == nil && conn == nil {
			err = fmt.Errorf("connection %q not found in %s", reference, filepath.Join(dir, "connections"))
		}
		return conn, err
	}), nil
}
