package main

import (
	gocontext "context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os/signal"
	"syscall"
	"time"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/commons-db/cmd/query/www"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/models"
	"github.com/spf13/cobra"
)

type serveOptions struct {
	host        string
	port        int
	profilesDir string
	dataDir     string
	dev         bool
	devURL      string
}

func newServeCmd() *cobra.Command {
	o := serveOptions{host: "localhost", port: 8080, profilesDir: "./profiles", dataDir: ".query/pg", devURL: "http://localhost:5173"}
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
	f.StringVar(&o.profilesDir, "profiles-dir", o.profilesDir, "Directory of profile YAML files")
	f.StringVar(&o.dataDir, "data-dir", o.dataDir, "Embedded postgres data directory")
	f.BoolVar(&o.dev, "dev", o.dev, "Proxy the UI to a running Vite dev server")
	f.StringVar(&o.devURL, "dev-url", o.devURL, "Vite dev server URL (with --dev)")
	return cmd
}

func runServe(cmd *cobra.Command, o serveOptions) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	dsn, stop, err := db.StartEmbedded(db.EmbeddedConfig{DataDir: o.dataDir})
	if err != nil {
		return fmt.Errorf("start embedded postgres: %w", err)
	}
	defer func() { _ = stop() }()

	gdb, pool, err := db.SetupDB(dsn, "query")
	if err != nil {
		return fmt.Errorf("setup db: %w", err)
	}
	if err := ensureSchema(gdb); err != nil {
		return err
	}
	if err := gdb.AutoMigrate(&models.Connection{}); err != nil {
		return fmt.Errorf("migrate connections: %w", err)
	}

	appCtx := dbcontext.NewContext(ctx).WithDB(gdb, pool).WithConnectionString(dsn)

	store, err := NewProfileStore(o.profilesDir)
	if err != nil {
		return err
	}

	// Register entities, then materialize the cobra command tree so the executor
	// (built by NewSwaggerServer from the root command) sees them.
	registerConnectionEntity(gdb)
	registerProfileEntity(store)
	clicky.GenerateCLI(cmd.Root())

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

	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	uiHandler, err := uiHandler(o)
	if err != nil {
		return err
	}
	mux.Handle("/", uiHandler)

	// Request pipeline (outer → inner): writes → profile execution → schema
	// content-negotiation → clicky executor + UI mux. Reads and discovery flow to
	// clicky; mutations, execution and schemas are owned by the wrappers.
	handler := newWriteHandler("/api/v1", gdb, store,
		newExecHandler("/api/v1", appCtx, store,
			newSchemaHandler("/api/v1", store, mux)))

	addr := fmt.Sprintf("%s:%d", o.host, o.port)
	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
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
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server: %w", err)
	}
	return nil
}

// uiHandler returns the SPA handler — a reverse proxy to the Vite dev server in
// --dev mode, otherwise the embedded build.
func uiHandler(o serveOptions) (http.Handler, error) {
	if o.dev {
		target, err := url.Parse(o.devURL)
		if err != nil {
			return nil, fmt.Errorf("invalid --dev-url: %w", err)
		}
		fmt.Printf("🔧 proxying UI to Vite dev server at %s\n", o.devURL)
		return httputil.NewSingleHostReverseProxy(target), nil
	}
	return www.Handler()
}
