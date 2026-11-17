package commands

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/flanksource/clicky"
	"github.com/flanksource/commons-db/cmd/query/internal/app"
	"github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/projections"
	"github.com/spf13/cobra"
)

type projectionOptions struct {
	Paths   []string `args:"true" help:"Projection files or directories"`
	Files   []string `flag:"file" short:"f" help:"Projection file or directory; repeatable"`
	Name    string   `flag:"name" help:"Select one exact metadata.name"`
	Context     string   `flag:"context" help:"Value supplied as context.name; does not select a query profile"`
	ContextFile string   `flag:"context-file" help:"YAML mapping whose keys are supplied as context.<key>, e.g. an assessment window"`
}

type projectionApplyOptions struct {
	projectionOptions
	DryRun bool `flag:"dry-run" help:"Execute sources and evaluate mappings without writing targets"`
}

func (o projectionOptions) load() ([]projections.Projection, error) {
	return projections.Load(projections.LoadOptions{Paths: append(append([]string{}, o.Files...), o.Paths...), Name: o.Name})
}

func (o projectionOptions) options(profiles projections.ProfileRunner) (projections.Options, error) {
	values := map[string]any{}
	if o.ContextFile != "" {
		var err error
		if values, err = projections.LoadContextFile(o.ContextFile); err != nil {
			return projections.Options{}, err
		}
	}
	if _, exists := values["name"]; exists && o.Context != "" {
		return projections.Options{}, fmt.Errorf("context.name is set by both --context and %s", o.ContextFile)
	}
	if o.Context != "" || values["name"] == nil {
		values["name"] = o.Context
	}
	return projections.Options{Context: values, Profiles: profiles}, nil
}

// projectionProfiles opens the query application only when a profile source
// runs, so verify and exec/http projections never load profiles or a database.
type projectionProfiles struct {
	options Options
	once    sync.Once
	service *profiles.Service
	err     error
}

func (p *projectionProfiles) RunProfile(ctx dbcontext.Context, request projections.ProfileRunRequest) (projections.ProfileRunResult, error) {
	p.once.Do(func() {
		var application *app.App
		application, p.err = app.New(app.Options{
			Args: p.options.Args, Stdout: p.options.Stdout, Stderr: p.options.Stderr,
			Database: app.ResolveDatabaseOptions(p.options.Args),
		})
		if p.err != nil {
			p.err = fmt.Errorf("initialize query application for profile sources: %w", p.err)
			return
		}
		p.service = application.Profiles
	})
	if p.err != nil {
		return projections.ProfileRunResult{}, p.err
	}
	return p.service.RunProfile(ctx, request)
}

func newProjections(options Options) *cobra.Command {
	runner := &projectionProfiles{options: options}
	root := &cobra.Command{Use: "projections", Short: "Project exec, HTTP or Query profile source rows into local YAML"}
	clicky.BindAllFlags(root.PersistentFlags(), "format", "tasks")
	clicky.AddNamedCommandWithContext("verify", root, projectionOptions{}, func(_ context.Context, o projectionOptions) (projections.ProjectionVerifyResult, error) {
		docs, err := o.load()
		if err != nil {
			return projections.ProjectionVerifyResult{}, err
		}
		return projections.Verify(docs)
	})
	clicky.AddNamedCommandWithContext("run", root, projectionOptions{}, func(ctx context.Context, o projectionOptions) ([]projections.ProjectionRunResult, error) {
		docs, err := o.load()
		if err != nil {
			return nil, err
		}
		options, err := o.options(runner)
		if err != nil {
			return nil, err
		}
		return projections.Run(dbcontext.NewContext(ctx), docs, options)
	})
	clicky.AddNamedCommandWithContext("apply", root, projectionApplyOptions{}, func(ctx context.Context, o projectionApplyOptions) ([]projections.ProjectionApplyResult, error) {
		docs, err := o.load()
		if err != nil {
			return nil, err
		}
		options, err := o.options(runner)
		if err != nil {
			return nil, err
		}
		options.DryRun = o.DryRun
		return projections.Apply(dbcontext.NewContext(ctx), docs, options)
	})
	root.AddCommand(&cobra.Command{Use: "syntax", Short: "Complete Projection syntax and runnable examples"})
	documentProjections(root)
	return root
}

func requestsProjections(args []string) bool {
	first := ""
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--":
			return false
		case flagsWithValues[arg]:
			i++
		case strings.HasPrefix(arg, "-"):
		case first == "" && arg == "help":
			first = arg
		default:
			return arg == "projections"
		}
	}
	return false
}
