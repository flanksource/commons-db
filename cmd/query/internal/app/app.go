package app

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/flanksource/commons-db/cmd/query/connections"
	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/cmd/query/sessions"
	"github.com/flanksource/commons-db/cmd/query/snapshots"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

type Options struct {
	Args   []string
	Stdout io.Writer
	Stderr io.Writer
	// Database selects the profile and connection store. A zero value keeps the
	// YAML file store, which is what metadata-only invocations pass so that
	// building the command tree never starts PostgreSQL.
	Database DatabaseOptions
}

type App struct {
	Runtime      *Runtime
	Connections  *connections.Service
	Profiles     *profiles.Service
	Sessions     *sessions.Runner
	fileStore    *profiles.FileStore
	snapshots    *snapshots.Manager
	profileStore profiles.StoreProvider
	stdout       io.Writer
	stderr       io.Writer
}

func New(options Options) (*App, error) {
	if options.Stdout == nil || options.Stderr == nil {
		return nil, fmt.Errorf("application output writers are required")
	}
	fileStore, err := profiles.NewFileStore(ResolveProfilesDir(options.Args))
	if err != nil {
		return nil, err
	}
	runtime, err := NewRuntime(dbcontext.NewContext(context.Background()), fileStore, options.Database)
	if err != nil {
		return nil, err
	}
	// Not under .tmp: snapshots now outlive the process that wrote them, and a
	// directory a restart deliberately preserves should not be named as scratch.
	snapshotManager, err := snapshots.New(snapshots.Options{
		Dir: filepath.Join(ResolveConfigDir(options.Args), "reconciliations"), MaxAge: time.Hour,
	})
	if err != nil {
		return nil, err
	}
	profileStore := func() (profiles.Store, error) {
		base, err := runtime.ProfileStore()
		if err != nil {
			return nil, err
		}
		return profiles.NewOverlayStore(base, snapshotManager)
	}
	if err := runtime.SetContext(runtime.Context().
		WithConnectionResolver(snapshotManager.ResolveConnection).
		WithConnectionLeaseResolver(snapshotManager.AcquireConnection)); err != nil {
		return nil, err
	}
	connectionService, err := connections.New(connections.Options{
		Database: runtime.Database, Context: runtime.Context, DecodeBody: DecodeBody,
		Virtual: snapshotManager,
		Profiles: func(ctx context.Context) ([]query.Profile, error) {
			store, err := profileStore()
			if err != nil {
				return nil, err
			}
			return store.List(ctx)
		},
	})
	if err != nil {
		return nil, err
	}
	profileService, err := profiles.New(profiles.Options{
		Store: profileStore, Context: runtime.Context, DecodeBody: DecodeBody, Snapshots: snapshotManager,
		OpenAPIExtensions: []profiles.OpenAPIExtension{connections.AddConnectionsOpenAPI},
	})
	if err != nil {
		return nil, err
	}
	runner, err := sessions.NewRunner(sessions.RunnerOptions{
		Profiles: profileStore, Context: runtime.Context, Stdout: options.Stdout, Stderr: options.Stderr,
	})
	if err != nil {
		return nil, err
	}
	return &App{
		Runtime: runtime, Connections: connectionService, Profiles: profileService, Sessions: runner,
		fileStore: fileStore, snapshots: snapshotManager, profileStore: profileStore,
		stdout: options.Stdout, stderr: options.Stderr,
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
