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
	"github.com/flanksource/commons-db/models"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
)

type serveOptions struct {
	host    string
	port    int
	dataDir string
	dev     bool
}

func newServeCmd() *cobra.Command {
	o := serveOptions{host: "localhost", port: 8080, dataDir: ".query/pg"}
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
	f.StringVar(&o.dataDir, "data-dir", o.dataDir, "Embedded postgres data directory")
	f.BoolVar(&o.dev, "dev", o.dev, "Spawn a Vite dev server (cmd/query/www) and proxy the UI to it")
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

	// Entities were registered (and the cobra tree generated) by shadowInit; serve
	// only injects the request-time dependencies they resolve lazily — the DB for
	// connection CRUD and the DB-backed context for profile execution.
	setDB(gdb)
	setContext(appCtx)
	store := currentStore()

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
	handler := newExecHandler("/api/v1", appCtx, store,
		newConnectionActionsHandler("/api/v1", appCtx,
			newSecretsHandler("/api/v1", kube,
				newSchemaHandler("/api/v1", store, mux))))

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
