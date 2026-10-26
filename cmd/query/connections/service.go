package connections

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/commons-db/cmd/query/secretcatalog"
	dbconnection "github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/observability"
	"github.com/flanksource/commons-db/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"k8s.io/client-go/kubernetes"
)

type DatabaseProvider func() (*gorm.DB, error)
type ContextProvider func() dbcontext.Context
type BodyDecoder func(context.Context, map[string]any) (map[string]any, error)

type VirtualConnectionProvider interface {
	// ListConnections fails when the host cannot read its catalog, rather than
	// listing it as empty.
	ListConnections() ([]*models.Connection, error)
	// ResolveConnection takes the context for the same reason
	// dbcontext.ConnectionResolver does: a reference alone does not always
	// identify a connection, and the same provider answers it here and there.
	ResolveConnection(dbcontext.Context, string) (*models.Connection, error)
}

type Options struct {
	// Database is the connection store. Without one, host-owned connections may
	// persist through a WritableConnectionProvider supplied as Virtual.
	Database   DatabaseProvider
	Context    ContextProvider
	DecodeBody BodyDecoder
	Profiles   ProfileProvider
	Virtual    VirtualConnectionProvider
	// Policy narrows the connection types and value sources the service lists,
	// offers in its form schema and accepts on save.
	Policy Policy
	// Secrets, when set, makes Handler serve the connection form's secret,
	// namespace, workload and 1Password pickers under {prefix}/connections.
	// Its Prefix and Next are set by Handler; an unset Context is the
	// service's, and an unset Kube that context's local Kubernetes client.
	Secrets *secretcatalog.Options
}

type Service struct {
	database   DatabaseProvider
	context    ContextProvider
	decodeBody BodyDecoder
	profiles   ProfileProvider
	virtual    VirtualConnectionProvider
	policy     Policy
	secrets    *secretcatalog.Options
	browser    *connectionBrowserHandler
}

func New(options Options) (*Service, error) {
	if options.Database == nil && options.Virtual == nil {
		return nil, fmt.Errorf("database provider or virtual connection provider is required")
	}
	if options.Context == nil {
		return nil, fmt.Errorf("context provider is required")
	}
	if options.DecodeBody == nil {
		return nil, fmt.Errorf("body decoder is required")
	}
	if options.Profiles == nil {
		return nil, fmt.Errorf("profile provider is required")
	}
	if err := options.Policy.validate(); err != nil {
		return nil, err
	}
	return &Service{
		database: options.Database, context: options.Context,
		decodeBody: options.DecodeBody, profiles: options.Profiles, virtual: options.Virtual,
		policy: options.Policy, secrets: options.Secrets,
		browser: newConnectionBrowserHandler("", options.Context(), http.NotFoundHandler()),
	}, nil
}

func (s *Service) SetVirtual(provider VirtualConnectionProvider) {
	s.virtual = provider
}

// ListOptions are the list/filter options for the connection entity. Types is
// the comma-separated form used by the connection lookup to scope a picker to the
// connection types valid for a profile's provider (e.g. sql → postgres,mysql,...).
type ListOptions struct {
	Type  string `flag:"type" help:"Filter by connection type"`
	Types string `flag:"types" help:"Filter by connection types (comma-separated)"`
}

// registerConnectionEntity registers the DB-backed connection entity with full
// CRUD on both the CLI and over HTTP. Create/Update use the context-aware
// handlers so they can read the raw nested JSON body (e.g. connection
// `properties`) clicky stashes on the context via rpc.RequestFromContext, which
// the executor would otherwise flatten to string flags. The database provider
// resolves lazily because entities register before the database exists.
func (s *Service) RegisterClicky() {
	registered := clicky.NewEntity[*models.Connection, ListOptions, *models.Connection]("connection").
		List(s.List).
		Get(s.Get).
		CreateWithContext(func(ctx context.Context, body map[string]any) (*models.Connection, error) {
			return s.Create(ctx, body)
		}).
		UpdateWithContext(func(ctx context.Context, id string, body map[string]any) (*models.Connection, error) {
			return s.Update(ctx, id, body)
		}).
		DeleteWithContext(func(_ context.Context, id string) error {
			return s.Delete(id)
		}).
		WithAction(entity.TypedActionWithContext("inspect", InspectFlags{service: s}, s.Inspect).
			WithShort("Inspect this connection's targets, field types, and query-dependent capabilities")).
		Filters(connectionFilter{service: s}).
		Build()
	registered.Routes = map[string]entity.RouteOverride{"update": {Path: "connection/{id}", Method: http.MethodPut}}
	entity.RegisterEntity(registered)
}

func (s *Service) Handler(prefix string, next http.Handler) http.Handler {
	ctx := s.context()
	browser := s.browser
	browser.prefix = strings.TrimRight(prefix, "/")
	browser.ctx = ctx
	browser.virtual = s.virtual
	actions := newConnectionActionsHandler(prefix, ctx, next)
	actions.virtual = s.virtual
	browser.next = actions
	health := newConnectionHealthHandler(prefix, ctx, browser)
	health.virtual = s.virtual
	handler := newConnectionSchemaHandler(browser.prefix, s.policy, newConnectionDashboardHandler(connectionDashboardHandlerOptions{
		Prefix: prefix, Context: ctx, Profiles: s.profiles, Virtual: s.virtual, Policy: s.policy, Next: health,
	}))
	if s.secrets == nil {
		return handler
	}
	return s.secretsHandler(browser.prefix, ctx, handler)
}

// secretsHandler serves the secret pickers' {prefix}/connections/secrets...,
// namespaces and workloads routes, in front of everything else Handler serves.
func (s *Service) secretsHandler(prefix string, ctx dbcontext.Context, next http.Handler) http.Handler {
	options := *s.secrets
	options.Prefix, options.Next = prefix+"/connections", next
	if options.Context.Context.Context == nil {
		options.Context = ctx
	}
	if options.Kube == nil {
		kube := options.Context
		options.Kube = func() (kubernetes.Interface, error) { return kube.LocalKubernetes() }
	}
	return secretcatalog.NewHandler(options)
}

func (s *Service) List(options ListOptions) ([]*models.Connection, error) {
	options, matches := s.policy.scope(options)
	if !matches {
		return []*models.Connection{}, nil
	}
	db, err := s.optionalStore()
	if err != nil {
		return nil, err
	}
	var stored []*models.Connection
	if db != nil {
		if stored, err = listConnections(db, options); err != nil {
			return nil, err
		}
	}
	return s.withVirtual(stored, options)
}

func (s *Service) Get(id string) (*models.Connection, error) {
	connection, err := s.findConnection(id)
	if err != nil {
		return nil, err
	}
	connection = cloneConnection(connection)
	redactConnection(connection)
	return connection, nil
}

func (s *Service) Create(ctx context.Context, body map[string]any) (*models.Connection, error) {
	if s.database == nil {
		return s.saveProvided(ctx, "", body)
	}
	db, err := s.store()
	if err != nil {
		return nil, err
	}
	body, err = s.decodeBody(ctx, body)
	if err != nil {
		return nil, err
	}
	return createConnection(db, s.policy, body)
}

func (s *Service) Update(ctx context.Context, id string, body map[string]any) (*models.Connection, error) {
	provided, err := resolveVirtualConnection(s.context(), s.virtual, id)
	if err != nil {
		return nil, err
	}
	if provided != nil {
		if provided.Virtual {
			return nil, entity.NewStatusErrorf(http.StatusMethodNotAllowed, "read_only", "virtual connection %q is read-only", provided.Name)
		}
		return s.saveProvided(ctx, id, body)
	}
	db, err := s.store()
	if err != nil {
		return nil, err
	}
	body, err = s.decodeBody(ctx, body)
	if err != nil {
		return nil, err
	}
	updated, err := updateConnection(db, s.policy, id, body)
	if err != nil {
		return nil, classifyReferenceConflict(err, "update", id)
	}
	forgetConnectionHealth(updated.ID.String())
	return updated, nil
}

func (s *Service) Delete(id string) error {
	provided, err := resolveVirtualConnection(s.context(), s.virtual, id)
	if err != nil {
		return err
	}
	if provided != nil {
		if provided.Virtual {
			return entity.NewStatusErrorf(http.StatusMethodNotAllowed, "read_only", "virtual connection %q is read-only", provided.Name)
		}
		writer, err := writableProvider(s.virtual)
		if err != nil {
			return err
		}
		if err := writer.DeleteConnection(s.context(), id); err != nil {
			return err
		}
		forgetConnectionHealth(provided.ID.String())
		return nil
	}
	db, err := s.store()
	if err != nil {
		return err
	}
	// Resolve first so both the delete and the health invalidation address the
	// same row even when the caller named the connection rather than its id.
	existing, err := findConnection(db, id)
	if err != nil {
		return err
	}
	if err := deleteConnection(db, existing.ID.String()); err != nil {
		return classifyReferenceConflict(err, "delete", existing.Name)
	}
	forgetConnectionHealth(existing.ID.String())
	return nil
}

// store returns the connection store a write needs, refusing as read-only when
// the host configured none: its whole catalog is virtual.
func (s *Service) store() (*gorm.DB, error) {
	if s.database == nil {
		return nil, entity.NewStatusError(http.StatusMethodNotAllowed, "read_only",
			"connections are read-only: no connection store is configured")
	}
	return s.database()
}

// optionalStore returns the connection store for a read, or nil when the host
// configured none and only its virtual connections exist.
func (s *Service) optionalStore() (*gorm.DB, error) {
	if s.database == nil {
		return nil, nil
	}
	return s.database()
}

func (s *Service) withVirtual(stored []*models.Connection, options ListOptions) ([]*models.Connection, error) {
	virtual, err := virtualConnections(s.virtual, options)
	if err != nil {
		return nil, err
	}
	for _, connection := range virtual {
		connection = cloneConnection(connection)
		redactConnection(connection)
		stored = append(stored, connection)
	}
	slices.SortFunc(stored, func(a, b *models.Connection) int { return strings.Compare(a.Name, b.Name) })
	return stored, nil
}

// virtualConnections returns the provider's connections that match the list's
// type filter, as the provider holds them. A connection that failed to load
// has no type to filter on, so it is always listed with its warnings.
func virtualConnections(provider VirtualConnectionProvider, options ListOptions) ([]*models.Connection, error) {
	if provider == nil {
		return nil, nil
	}
	listed, err := provider.ListConnections()
	if err != nil {
		return nil, fmt.Errorf("list virtual connections: %w", err)
	}
	types := connectionTypeFilter(options)
	var matched []*models.Connection
	for _, connection := range listed {
		if len(types) == 0 || slices.Contains(types, connection.Type) || len(connection.Warnings) > 0 && connection.Type == "" {
			matched = append(matched, connection)
		}
	}
	return matched, nil
}

func resolveVirtualConnection(ctx dbcontext.Context, provider VirtualConnectionProvider, id string) (*models.Connection, error) {
	if provider == nil {
		return nil, nil
	}
	return provider.ResolveConnection(ctx, id)
}

// lookupConnection resolves an id the way every connection surface does: the
// host's virtual connections first, then the store when there is one (db nil
// means there is none).
func lookupConnection(ctx dbcontext.Context, provider VirtualConnectionProvider, db *gorm.DB, id string) (*models.Connection, error) {
	virtual, err := resolveVirtualConnection(ctx, provider, id)
	if err != nil || virtual != nil {
		return virtual, err
	}
	if db == nil {
		return nil, entity.NewStatusErrorf(http.StatusNotFound, "not_found", "connection %q not found", id)
	}
	return findConnection(db, id)
}

func (s *Service) findConnection(id string) (*models.Connection, error) {
	db, err := s.optionalStore()
	if err != nil {
		return nil, err
	}
	return lookupConnection(s.context(), s.virtual, db, id)
}

type resolveConnectionOptions struct {
	Context context.Context
	ID      string
	// RefreshSecrets drops the cached env values the connection resolves through,
	// so a rotated secret is read again instead of being served from the env
	// cache for the rest of its TTL.
	RefreshSecrets bool
}

// resolveConnection returns the stored connection row and a hydrated clone of it.
//
// Anything that dials the connection needs the clone: a stored field is a
// `secret://<name>/<key>` reference, not an address, and the SQL and OpenSearch
// clients take the URL verbatim. The raw row comes back alongside because
// sanitizeConnectionError needs both — the raw row to redact what the operator
// configured, the resolved one to redact what hydration just put in memory.
//
// This is the CLI counterpart of findConnectionMust, routed through
// Service.findConnection so a virtual connection resolves from s.virtual rather
// than requiring a context-level connection resolver.
func (s *Service) resolveConnection(options resolveConnectionOptions) (*models.Connection, *models.Connection, error) {
	raw, err := s.findConnection(options.ID)
	if err != nil {
		return nil, nil, err
	}

	connectionContext := s.context().Wrap(options.Context)
	if options.RefreshSecrets {
		// Invalidation reads the references off the stored row, so it has to run
		// before hydration replaces them with values.
		if err := dbcontext.InvalidateConnectionSecrets(connectionContext, raw); err != nil {
			connectionContext.Logger.V(3).Infof("connection %s: invalidate secret cache: %s", raw.Name, err)
		}
	}

	resolved := cloneConnection(raw)
	if _, err := dbcontext.HydrateConnection(connectionContext, resolved); err != nil {
		return nil, nil, fmt.Errorf("resolve connection %q: %s", raw.Name, sanitizeConnectionError(err, raw))
	}
	return raw, resolved, nil
}

func Parse(body map[string]any) (*models.Connection, error) {
	return connectionFromBody(body)
}

// listConnections returns redacted connections, optionally filtered by type(s).
func listConnections(db *gorm.DB, o ListOptions) ([]*models.Connection, error) {
	q := db.Model(&models.Connection{})
	if types := connectionTypeFilter(o); len(types) > 0 {
		q = q.Where("type IN ?", types)
	}
	var conns []*models.Connection
	if err := q.Order("name").Find(&conns).Error; err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	for _, c := range conns {
		redactConnection(c)
	}
	return conns, nil
}

// connectionTypeFilter collects the type filter from the single `type` flag and
// the comma-separated `types` scope used by the lookup, de-duplicating blanks.
func connectionTypeFilter(o ListOptions) []string {
	var types []string
	if o.Type != "" {
		types = append(types, o.Type)
	}
	for _, t := range strings.Split(o.Types, ",") {
		if t = strings.TrimSpace(t); t != "" {
			types = append(types, t)
		}
	}
	return types
}

// createConnection inserts a new connection and returns it redacted.
func createConnection(db *gorm.DB, policy Policy, body map[string]any) (*models.Connection, error) {
	c, err := connectionFromBody(body)
	if err != nil {
		return nil, err
	}
	if err := policy.check(c); err != nil {
		return nil, err
	}
	if err := validateConnection(dbcontext.New().WithDB(db, nil), c); err != nil {
		return nil, err
	}
	c.ID = uuid.Nil // let the DB assign the id
	if err := db.Create(c).Error; err != nil {
		return nil, fmt.Errorf("create connection: %w", err)
	}
	redactConnection(c)
	return c, nil
}

// updateConnection applies an edit, preserving stored secrets left blank.
func updateConnection(db *gorm.DB, policy Policy, id string, body map[string]any) (*models.Connection, error) {
	existing, err := findConnection(db, id)
	if err != nil {
		return nil, err
	}
	incoming, err := connectionFromBody(body)
	if err != nil {
		return nil, err
	}
	if err := validateConnection(dbcontext.New().WithDB(db, nil), incoming); err != nil {
		return nil, err
	}
	applyConnectionUpdate(existing, incoming)
	if err := policy.check(existing); err != nil {
		return nil, err
	}
	if err := db.Save(existing).Error; err != nil {
		return nil, fmt.Errorf("update connection: %w", err)
	}
	redactConnection(existing)
	return existing, nil
}

func validateConnection(ctx dbcontext.Context, candidate *models.Connection) error {
	if _, err := observability.PolicyFor(candidate); err != nil {
		return err
	}
	return validateNestedConnection(ctx, candidate)
}

func validateNestedConnection(ctx dbcontext.Context, candidate *models.Connection) error {
	if candidate.Type != models.ConnectionTypeOpenTelemetry {
		return nil
	}
	openTelemetry, err := dbconnection.NewOpenTelemetry(candidate)
	if err != nil {
		return err
	}
	nested, err := dbcontext.HydrateConnectionByURL(ctx, openTelemetry.Connection)
	if err != nil {
		return fmt.Errorf("resolve nested OpenSearch connection: %w", err)
	}
	if nested == nil {
		return fmt.Errorf("nested OpenSearch connection %q not found", openTelemetry.Connection)
	}
	if nested.Type != models.ConnectionTypeOpenSearch {
		return fmt.Errorf("nested connection %q has type %q, expected %q", nested.Name, nested.Type, models.ConnectionTypeOpenSearch)
	}
	return nil
}

// deleteConnection removes a connection by id, erroring when absent.
func deleteConnection(db *gorm.DB, id string) error {
	res := db.Where("id = ?", id).Delete(&models.Connection{})
	if res.Error != nil {
		return fmt.Errorf("delete connection: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("connection %q not found", id)
	}
	return nil
}

// findConnection resolves a connection by UUID, falling back to its name.
func findConnection(db *gorm.DB, id string) (*models.Connection, error) {
	var c models.Connection
	q := db.Model(&models.Connection{})
	if _, err := uuid.Parse(id); err == nil {
		q = q.Where("id = ?", id)
	} else {
		q = q.Where("name = ?", id)
	}
	if err := q.First(&c).Error; err != nil {
		return nil, fmt.Errorf("connection %q not found: %w", id, err)
	}
	return &c, nil
}

// connectionFromBody decodes a request body into a Connection, failing fast on a
// missing name or type. The body's id is dropped: create lets the DB assign one
// and update addresses the row by path/flag id, so a body id (which may be a name
// on update) must never be parsed into the uuid ID field.
func connectionFromBody(body map[string]any) (*models.Connection, error) {
	delete(body, "id")
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode connection body: %w", err)
	}
	var c models.Connection
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("invalid connection: %w", err)
	}
	if c.Name == "" {
		return nil, fmt.Errorf("connection name is required")
	}
	if c.Type == "" {
		return nil, fmt.Errorf("connection type is required")
	}
	return &c, nil
}

// applyConnectionUpdate copies editable fields from incoming onto existing,
// preserving stored secrets when the incoming payload leaves them blank.
func applyConnectionUpdate(existing, incoming *models.Connection) {
	existing.Name = incoming.Name
	existing.Namespace = incoming.Namespace
	existing.Type = incoming.Type
	existing.URL = incoming.URL
	existing.Username = incoming.Username
	existing.Properties = incoming.Properties
	existing.InsecureTLS = incoming.InsecureTLS
	if incoming.Password != "" {
		existing.Password = incoming.Password
	}
	if incoming.Certificate != "" {
		existing.Certificate = incoming.Certificate
	}
}

// redactConnection blanks literal credentials while preserving source references.
func redactConnection(c *models.Connection) {
	if c == nil {
		return
	}
	if isHTTPAuthConnectionType(c.Type) {
		c.Properties = maps.Clone(c.Properties)
		if c.Properties == nil {
			c.Properties = map[string]string{}
		}
		if c.Properties["authType"] == "" {
			c.Properties["authType"] = inferHTTPAuthType(c)
		}
	}
	c.Password = redactCredential(c.Password)
	c.Certificate = redactCredential(c.Certificate)
	if c.Virtual {
		c.URL = ""
	}
}

func redactCredential(value string) string {
	var ref types.EnvVar
	if err := ref.Scan(value); err == nil && ref.ValueFrom != nil {
		return value
	}
	return ""
}

func isHTTPAuthConnectionType(connectionType string) bool {
	switch connectionType {
	case models.ConnectionTypeHTTP, models.ConnectionTypeOpenSearch, models.ConnectionTypeElasticSearch,
		models.ConnectionTypePrometheus, models.ConnectionTypeLoki, models.ConnectionTypeJaeger:
		return true
	default:
		return false
	}
}

func inferHTTPAuthType(c *models.Connection) string {
	if c.Properties["authType"] != "" {
		return c.Properties["authType"]
	}
	if c.Properties["cert"] != "" || c.Properties["key"] != "" {
		return "mtls"
	}
	if c.Properties["clientID"] != "" || c.Properties["clientSecret"] != "" || c.Properties["tokenURL"] != "" {
		return "oauth"
	}
	if c.Username != "" || c.Password != "" || c.Properties["username"] != "" || c.Properties["password"] != "" {
		return "basic"
	}
	return "none"
}
