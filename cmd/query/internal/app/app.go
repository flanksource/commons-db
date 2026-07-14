package app

import (
	"context"
	"fmt"
	"io"

	"github.com/flanksource/commons-db/cmd/query/connections"
	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/cmd/query/sessions"
	dbcontext "github.com/flanksource/commons-db/context"
)

type Options struct {
	Args   []string
	Stdout io.Writer
	Stderr io.Writer
}

type App struct {
	Runtime     *Runtime
	Connections *connections.Service
	Profiles    *profiles.Service
	Sessions    *sessions.Runner
	fileStore   *profiles.FileStore
	stdout      io.Writer
	stderr      io.Writer
}

func New(options Options) (*App, error) {
	if options.Stdout == nil || options.Stderr == nil {
		return nil, fmt.Errorf("application output writers are required")
	}
	fileStore, err := profiles.NewFileStore(ResolveProfilesDir(options.Args))
	if err != nil {
		return nil, err
	}
	runtime, err := NewRuntime(dbcontext.NewContext(context.Background()), fileStore)
	if err != nil {
		return nil, err
	}
	connectionService, err := connections.New(connections.Options{
		Database: runtime.Database, Context: runtime.Context, DecodeBody: DecodeBody,
	})
	if err != nil {
		return nil, err
	}
	profileService, err := profiles.New(profiles.Options{
		Store: runtime.ProfileStore, Context: runtime.Context, DecodeBody: DecodeBody,
	})
	if err != nil {
		return nil, err
	}
	runner, err := sessions.NewRunner(sessions.RunnerOptions{
		Profiles: runtime.ProfileStore, Context: runtime.Context, Stdout: options.Stdout, Stderr: options.Stderr,
	})
	if err != nil {
		return nil, err
	}
	return &App{
		Runtime: runtime, Connections: connectionService, Profiles: profileService, Sessions: runner,
		fileStore: fileStore, stdout: options.Stdout, stderr: options.Stderr,
	}, nil
}

func (a *App) RegisterEntities(ctx context.Context) error {
	a.Connections.RegisterClicky()
	a.Profiles.RegisterClicky()
	if err := a.Profiles.RegisterDynamic(ctx); err != nil {
		return fmt.Errorf("register profile entities: %w", err)
	}
	return nil
}

func (a *App) RunTrace(ctx context.Context, name string, options sessions.TraceOptions) error {
	return a.Sessions.RunTrace(ctx, name, options)
}

func (a *App) RunTop(ctx context.Context, name string, options sessions.TopOptions) error {
	return a.Sessions.RunTop(ctx, name, options)
}
