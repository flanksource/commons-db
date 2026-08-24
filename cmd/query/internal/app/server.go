package app

import (
	"context"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/flanksource/clicky/rpc"
	rpchttp "github.com/flanksource/clicky/rpc/http"
	"github.com/flanksource/commons-db/cmd/query/devtools"
	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/cmd/query/sessions"
	"github.com/flanksource/commons-db/cmd/query/www"
	dbcontext "github.com/flanksource/commons-db/context"
	dutyKubernetes "github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons-db/query"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
)

type ServeOptions struct {
	Host                    string
	Port                    int
	DatabaseURL             string
	DataDir                 string
	Dev                     bool
	HideErrorDetails        bool
	MaxSessions             int
	MaxSessionDuration      time.Duration
	SessionRetention        time.Duration
	ReconcileSnapshotMaxAge time.Duration
}

func DefaultServeOptions() ServeOptions {
	return ServeOptions{
		Host: "localhost", Port: 8080, DatabaseURL: EmbeddedDatabase, MaxSessions: 5,
		MaxSessionDuration: 15 * time.Minute, SessionRetention: 7 * 24 * time.Hour,
		ReconcileSnapshotMaxAge: time.Hour,
	}
}

func (a *App) Serve(parent context.Context, root *cobra.Command, configDir string, options ServeOptions) error {
	if root == nil {
		return fmt.Errorf("root command is required")
	}
	ctx, cancel := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	defer dutyKubernetes.DefaultForwardManager().CloseAll()

	configDir = NormalizeConfigDir(configDir)
	if err := ensurePrivateDir(configDir); err != nil {
		return fmt.Errorf("create config dir %q: %w", configDir, err)
	}
	// Serve owns the cluster it starts, so unlike a sub-command it stops it on the
	// way out. Rebinding the context first keeps the signal-aware ctx underneath
	// the database handles EnsureDatabase layers on.
	database := DatabaseOptions{URL: options.DatabaseURL, DataDir: resolveDataDir(configDir, options.DataDir)}
	if !database.Enabled() {
		return fmt.Errorf("serve requires a database; --db is empty")
	}
	if options.ReconcileSnapshotMaxAge <= 0 {
		return fmt.Errorf("reconcile snapshot maximum age must be positive")
	}
	// SetMaxAge before Prepare, and the order is load-bearing: SetMaxAge refuses
	// while snapshots exist, and Prepare is what reloads them from disk. Swapped,
	// a server that had ever written a snapshot would fail to start.
	if err := a.snapshots.SetMaxAge(options.ReconcileSnapshotMaxAge); err != nil {
		return err
	}
	if err := a.snapshots.Prepare(); err != nil {
		return err
	}
	defer func() { _ = a.snapshots.Close() }()
	if err := a.Runtime.SetContext(dbcontext.NewContext(ctx).
		WithConnectionResolver(a.snapshots.ResolveConnection).
		WithConnectionLeaseResolver(a.snapshots.AcquireConnection)); err != nil {
		return err
	}
	a.Runtime.SetDatabaseOptions(database)
	if err := a.Runtime.EnsureDatabase(ctx); err != nil {
		return err
	}
	defer func() { _ = a.Runtime.Close() }()

	gdb, err := a.Runtime.Database()
	if err != nil {
		return err
	}
	queryContext := a.Runtime.Context()
	store, err := a.Runtime.ProfileStore()
	if err != nil {
		return err
	}
	databaseProfiles, ok := store.(*profiles.DBStore)
	if !ok {
		return fmt.Errorf("serve requires a database-backed profile store, got %T", store)
	}
	if err := profiles.Import(ctx, a.fileStore, databaseProfiles); err != nil {
		return err
	}
	if err := a.Profiles.RegisterDynamic(ctx); err != nil {
		return fmt.Errorf("register database profiles: %w", err)
	}

	sessionStore, err := sessions.NewStore(gdb, options.SessionRetention)
	if err != nil {
		return err
	}
	defer func() { _ = sessionStore.Close() }()
	if err := sessionStore.MarkInterrupted(ctx); err != nil {
		return err
	}
	if err := sessionStore.Prune(ctx); err != nil {
		return err
	}
	sessionRegistry := query.NewSessionRegistry(query.RegistryOptions{
		MaxSessions: options.MaxSessions, MaxDuration: options.MaxSessionDuration,
		OnEvent: sessionStore.OnEvent, OnTransition: sessionStore.OnTransition,
	})
	sessionStore.BindResolver(sessionRegistry.Get)
	defer sessionRegistry.StopAll()

	// The devtools record store is memory-only by design: a record holds request
	// and response bodies, which must not outlive the process that was asked to
	// capture them.
	devtoolsStore := devtools.NewStore(devtools.Options{})
	if !options.HideErrorDetails {
		// Tee the process logger so the console's Console tab sees background work
		// — reconciles, snapshot refreshes, unarmed requests — that no request-scoped
		// recorder ever sees. The original writer stays in the chain, so the
		// operator's terminal is unchanged.
		restore := devtools.TeeProcessLogs(devtoolsStore)
		defer restore()
	}

	server := rpc.NewSwaggerServer(
		&rpc.ServeConfig{
			Host: options.Host, Port: options.Port, Title: "Query", Version: "0.1.0", SkipHealth: false,
			HideErrorDetails: options.HideErrorDetails,
			Executor:         &rpc.ExecutorConfig{Enabled: true, SkipPreRun: true, PathPrefix: "/api/v1"},
		},
		root,
		&rpc.OpenAPIConfig{Title: "Query", Description: "Connections, profiles and execution", Version: "0.1.0"},
	)
	serverMux := http.NewServeMux()
	server.RegisterRoutes(serverMux)
	mux := http.NewServeMux()
	openAPI, err := a.Profiles.OpenAPIHandler(root, server.ConverterConfig())
	if err != nil {
		return err
	}
	mux.Handle("/api/openapi.json", openAPI)
	chat, err := newQueryChatServer(root)
	if err != nil {
		return err
	}
	mux.Handle("/api/chat", chat.Handler())
	mux.Handle("/api/chat/", chat.Handler())
	mux.Handle("/api/", serverMux)
	mux.Handle("/health", serverMux)

	var ui http.Handler
	if options.Dev {
		proxy, cleanup, err := startViteDevProxy(ctx, options.Host, options.Port)
		if err != nil {
			return err
		}
		defer cleanup()
		ui = proxy
	} else {
		ui, err = www.Handler()
		if err != nil {
			return err
		}
	}
	mux.Handle("/", ui)

	kube := func() (kubernetes.Interface, error) { return queryContext.LocalKubernetes() }
	base := newSecretsHandler(secretsHandlerOptions{
		Prefix: "/api/v1", Context: queryContext, Kube: kube,
		Next: newSchemaHandler("/api/v1", databaseProfiles, mux),
	})
	connectionHandler := a.Connections.Handler("/api/v1", base)
	profileHandler, err := a.Profiles.Handler("/api/v1", connectionHandler)
	if err != nil {
		return err
	}
	sessionService, err := sessions.New(sessions.Options{
		Profiles: func() (profiles.Store, error) { return a.profileStore() },
		Context:  a.Runtime.Context, Registry: sessionRegistry, Store: sessionStore,
	})
	if err != nil {
		return err
	}
	handler, err := sessionService.Handler("/api/v1", profileHandler)
	if err != nil {
		return err
	}

	// Devtools sits outermost because it is a router and a middleware at once: it
	// serves its own routes, and it has to wrap everything downstream to arm a
	// request before the handler runs and file the record once it returns.
	//
	// A server told to hide error details is not one that may hand out queries,
	// headers and response bodies through a side door, so it shares that switch.
	devtoolsHandler := devtools.New(devtools.HandlerOptions{
		Prefix: "/api/v1", Store: devtoolsStore, Enabled: !options.HideErrorDetails,
	}).Handler(handler)

	address := fmt.Sprintf("%s:%d", options.Host, options.Port)
	httpServer := &http.Server{
		// TimingMiddleware goes inside Compress so `total` covers the handler, and
		// it forwards Flush, which the event streams need.
		Addr: address, Handler: rpc.Compress(rpchttp.TimingMiddleware(devtoolsHandler)),
		ReadTimeout: 30 * time.Second, WriteTimeout: 0, IdleTimeout: 90 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownContext, shutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdown()
		_ = httpServer.Shutdown(shutdownContext)
	}()

	fmt.Fprintf(a.stdout, "🚀 query serve on http://%s\n", address)
	fmt.Fprintf(a.stdout, "📄 OpenAPI: http://%s/api/openapi.json\n", address)
	fmt.Fprintf(a.stdout, "🤖 AI Chat: http://%s/api/chat\n", address)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server: %w", err)
	}
	return nil
}
