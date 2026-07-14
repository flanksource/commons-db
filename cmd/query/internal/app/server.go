package app

import (
	"context"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/cmd/query/sessions"
	"github.com/flanksource/commons-db/cmd/query/www"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/db"
	dutyKubernetes "github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons-db/query"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
)

type ServeOptions struct {
	Host               string
	Port               int
	DataDir            string
	Dev                bool
	MaxSessions        int
	MaxSessionDuration time.Duration
	SessionRetention   time.Duration
}

func DefaultServeOptions() ServeOptions {
	return ServeOptions{
		Host: "localhost", Port: 8080, MaxSessions: 5,
		MaxSessionDuration: 15 * time.Minute, SessionRetention: 7 * 24 * time.Hour,
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
	dsn, stop, err := db.StartEmbedded(db.EmbeddedConfig{DataDir: resolveDataDir(configDir, options.DataDir)})
	if err != nil {
		return fmt.Errorf("start embedded postgres: %w", err)
	}
	defer func() { _ = stop() }()

	gdb, pool, err := db.SetupDB(dsn, "query")
	if err != nil {
		return fmt.Errorf("setup db: %w", err)
	}
	defer pool.Close()
	if err := migrateSchema(ctx, dsn); err != nil {
		return err
	}

	queryContext := dbcontext.NewContext(ctx).WithDB(gdb, pool).WithConnectionString(dsn)
	databaseProfiles, err := profiles.NewDBStore(gdb)
	if err != nil {
		return err
	}
	if err := profiles.Import(ctx, a.fileStore, databaseProfiles); err != nil {
		return err
	}
	if err := a.Runtime.SetDatabase(gdb); err != nil {
		return err
	}
	if err := a.Runtime.SetContext(queryContext); err != nil {
		return err
	}
	if err := a.Runtime.SetProfileStore(databaseProfiles); err != nil {
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

	server := rpc.NewSwaggerServer(
		&rpc.ServeConfig{
			Host: options.Host, Port: options.Port, Title: "Query", Version: "0.1.0", SkipHealth: false,
			Executor: &rpc.ExecutorConfig{Enabled: true, SkipPreRun: true, PathPrefix: "/api/v1"},
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
	chat := newQueryChatServer(root)
	defer func() { _ = chat.Close() }()
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
	base := newSecretsHandler("/api/v1", queryContext, kube, newSchemaHandler("/api/v1", databaseProfiles, mux))
	connectionHandler := a.Connections.Handler("/api/v1", base)
	profileHandler, err := a.Profiles.Handler("/api/v1", connectionHandler)
	if err != nil {
		return err
	}
	sessionService, err := sessions.New(sessions.Options{
		Profiles: a.Runtime.ProfileStore, Context: a.Runtime.Context, Registry: sessionRegistry, Store: sessionStore,
	})
	if err != nil {
		return err
	}
	handler, err := sessionService.Handler("/api/v1", profileHandler)
	if err != nil {
		return err
	}

	address := fmt.Sprintf("%s:%d", options.Host, options.Port)
	httpServer := &http.Server{
		Addr: address, Handler: handler, ReadTimeout: 30 * time.Second, WriteTimeout: 0, IdleTimeout: 90 * time.Second,
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
