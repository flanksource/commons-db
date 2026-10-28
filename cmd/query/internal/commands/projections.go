package commands

import (
	"context"
	"strings"

	"github.com/flanksource/clicky"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/projections"
	"github.com/spf13/cobra"
)

type projectionOptions struct {
	Paths   []string `args:"true" help:"Projection files or directories"`
	Files   []string `flag:"file" short:"f" help:"Projection file or directory; repeatable"`
	Name    string   `flag:"name" help:"Select one exact metadata.name"`
	Context string   `flag:"context" help:"Value supplied as context.name; does not select a query profile"`
}

type projectionApplyOptions struct {
	projectionOptions
	DryRun bool `flag:"dry-run" help:"Execute sources and evaluate mappings without writing targets"`
}

func (o projectionOptions) load() ([]projections.Projection, error) {
	return projections.Load(projections.LoadOptions{Paths: append(append([]string{}, o.Files...), o.Paths...), Name: o.Name})
}

func (o projectionOptions) options() projections.Options {
	return projections.Options{Context: map[string]any{"name": o.Context}}
}

func newProjections() *cobra.Command {
	root := &cobra.Command{Use: "projections", Short: "Project exec or HTTP source rows into local YAML"}
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
		return projections.Run(dbcontext.NewContext(ctx), docs, o.options())
	})
	clicky.AddNamedCommandWithContext("apply", root, projectionApplyOptions{}, func(ctx context.Context, o projectionApplyOptions) ([]projections.ProjectionApplyResult, error) {
		docs, err := o.load()
		if err != nil {
			return nil, err
		}
		options := o.options()
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
