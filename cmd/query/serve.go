package main

import (
	gocontext "context"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/commons-db/cmd/query/www"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/db"
	dutyKubernetes "github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons-db/query"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
)

type serveOptions struct {
	host               string
	port               int
	dataDir            string
	dev                bool
	maxSessions        int
	maxSessionDuration time.Duration
	sessionRetention   time.Duration
}

func newServeCmd() *cobra.Command {
	o := serveOptions{host: "localhost", port: 8080}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the query web app (connections, profiles, execution)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(cmd, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.host, "host", o.host, "Host to bind")
	f.IntVarP(&o.port, "port", "p", o.port, "Port to bind")
	f.StringVar(&o.dataDir, "data-dir", o.dataDir, "Embedded postgres data directory (default: <config-dir>/postgres)")
	f.BoolVar(&o.dev, "dev", o.dev, "Spawn a Vite dev server (cmd/query/www) and proxy the UI to it")
	f.IntVar(&o.maxSessions, "max-sessions", 5, "Maximum concurrently running trace/top sessions")
	f.DurationVar(&o.maxSessionDuration, "max-session-duration", 15*time.Minute, "Upper bound on any trace/top session; profiles can only lower it")
	f.DurationVar(&o.sessionRetention, "session-retention", 7*24*time.Hour, "How long finished sessions and their events are kept in PostgreSQL")
	return cmd
}

func runServe(cmd *cobra.Command, o serveOptions) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	defer dutyKubernetes.DefaultForwardManager().CloseAll()

	configDir, err := cmd.Root().PersistentFlags().GetString("config-dir")
	if err != nil {
		return fmt.Errorf("read config-dir: %w", err)
	}
	if configDir == "" {
		configDir = defaultQueryConfigDir()
	}
	if err := ensurePrivateDir(configDir); err != nil {
		return fmt.Errorf("create config dir %q: %w", configDir, err)
	}
	dataDir := resolveDataDir(configDir, o.dataDir)

	dsn, stop, err := db.StartEmbedded(db.EmbeddedConfig{DataDir: dataDir})
	if err != nil {
		return fmt.Errorf("start embedded postgres: %w", err)
	}
	defer func() { _ = stop() }()

	gdb, pool, err := db.SetupDB(dsn, "query")
	if err != nil {
		return fmt.Errorf("setup db: %w", err)
	}
	if err := migrateSchema(ctx, dsn); err != nil {
		return err
	}

	appCtx := dbcontext.NewContext(ctx).WithDB(gdb, pool).WithConnectionString(dsn)

	// Entities were registered (and the cobra tree generated) by shadowInit; serve
	// only injects the request-time dependencies they resolve lazily — the DB for
	// connection CRUD and the DB-backed context for profile execution.
	setDB(gdb)
	setContext(appCtx)
	store := currentStore()
	if err := store.UseDB(gdb); err != nil {
		return fmt.Errorf("initialize profile database store: %w", err)
	}
	if err := registerProfileEntities(store); err != nil {
		return fmt.Errorf("register database profiles: %w", err)
	}

	// Trace/top sessions: durable record in PostgreSQL, live registry in
	// memory. Sessions orphaned by the previous process are finalized as
	// interrupted, and expired ones pruned, before new ones start.
	sessionStore := NewSessionStore(gdb, o.sessionRetention)
	defer func() { _ = sessionStore.Close() }()
	if err := sessionStore.MarkInterrupted(); err != nil {
		return err
	}
	if err := sessionStore.Prune(); err != nil {
		return err
	}
	sessionRegistry := query.NewSessionRegistry(query.RegistryOptions{
		MaxSessions:  o.maxSessions,
		MaxDuration:  o.maxSessionDuration,
		OnEvent:      sessionStore.OnEvent,
		OnTransition: sessionStore.OnTransition,
	})
	sessionStore.BindResolver(sessionRegistry.Get)
	defer sessionRegistry.StopAll()

	server := rpc.NewSwaggerServer(
		&rpc.ServeConfig{
			Host:       o.host,
			Port:       o.port,
			Title:      "Query",
			Version:    "0.1.0",
			SkipHealth: false,
			Executor:   &rpc.ExecutorConfig{Enabled: true, SkipPreRun: true, PathPrefix: "/api/v1"},
		},
		cmd.Root(),
		&rpc.OpenAPIConfig{Title: "Query", Description: "Connections, profiles and execution", Version: "0.1.0"},
	)

	serverMux := http.NewServeMux()
	server.RegisterRoutes(serverMux)
	mux := http.NewServeMux()
	mux.Handle("/api/openapi.json", newProfileOpenAPIHandler(cmd.Root(), server.ConverterConfig(), store))
	chat := newQueryChatServer(cmd.Root())
	defer func() { _ = chat.Close() }()
	mux.Handle("/api/chat", chat.Handler())
	mux.Handle("/api/chat/", chat.Handler())
	mux.Handle("/api/", serverMux)
	mux.Handle("/health", serverMux)

	// In --dev the binary spawns Vite and proxies the UI to it (live HMR);
	// otherwise it serves the embedded production build.
	var ui http.Handler
	if o.dev {
		proxy, cleanup, err := startViteDevProxy(ctx, o.host, o.port)
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

	// Request pipeline (outer → inner): profile execution → secret listing →
	// schema content-negotiation → clicky executor + UI mux. Reads, discovery and
	// all connection/profile CRUD flow through clicky entities; execution, the
	// SecretKeySelector data source, and schemas are owned by the wrappers.
	kube := func() (kubernetes.Interface, error) {
		c, err := appCtx.LocalKubernetes()
		if err != nil {
			return nil, err
		}
		return c, nil
	}
	handler := newSessionHandler(sessionHandlerOptions{
		Prefix:   "/api/v1",
		Ctx:      appCtx,
		Store:    store,
		Registry: sessionRegistry,
		Sessions: sessionStore,
		Next: newExecHandler("/api/v1", appCtx, store,
			newProfileSampleHandler("/api/v1", appCtx,
				newConnectionBrowserHandler("/api/v1", appCtx,
					newConnectionActionsHandler("/api/v1", appCtx,
						newSecretsHandler("/api/v1", appCtx, kube,
							newSchemaHandler("/api/v1", store, mux)))))),
	})

	addr := fmt.Sprintf("%s:%d", o.host, o.port)
	httpSrv := &http.Server{
		Addr:        addr,
		Handler:     handler,
		ReadTimeout: 30 * time.Second,
		// Chat responses use a long-lived AI SDK stream. A server-wide write
		// timeout would truncate otherwise healthy assistant turns.
		WriteTimeout: 0,
		IdleTimeout:  90 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, c := gocontext.WithTimeout(gocontext.Background(), 5*time.Second)
		defer c()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	fmt.Printf("🚀 query serve on http://%s\n", addr)
	fmt.Printf("📄 OpenAPI: http://%s/api/openapi.json\n", addr)
	fmt.Printf("🤖 AI Chat: http://%s/api/chat\n", addr)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server: %w", err)
	}
	return nil
}
