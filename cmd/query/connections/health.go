package connections

import (
	gocontext "context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/patrickmn/go-cache"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

const (
	connectionHealthTTL          = 5 * time.Minute
	connectionProbeConcurrency   = 6
	connectionProbeTimeout       = 5 * time.Second
	connectionHealthBatchTimeout = 30 * time.Second
	maxHealthBatch               = 200
)

type connectionHealthState string

const (
	connectionHealthHealthy      connectionHealthState = "healthy"
	connectionHealthCredentials  connectionHealthState = "credentials"
	connectionHealthUnreachable  connectionHealthState = "unreachable"
	connectionHealthUnverifiable connectionHealthState = "unverifiable"
	// connectionHealthUnknown means the batch budget expired before the probe
	// finished. It is indeterminate, so it is never cached.
	connectionHealthUnknown connectionHealthState = "unknown"
)

// connectionHealthCache holds probe outcomes so listing a fleet costs a map
// lookup rather than a connection per row. It is keyed by connection id alone —
// the row's UpdatedAt lives inside the value and is compared on read, which
// gives edit-invalidation without an unbounded key space.
var (
	connectionHealthCache = cache.New(connectionHealthTTL, 2*connectionHealthTTL)
	connectionHealthGroup singleflight.Group
)

// connectionHealthResult is the superset the dashboard row and the connection
// info header both project from, so a probe runs once and serves both.
type connectionHealthResult struct {
	ID                  string
	State               connectionHealthState
	Detail              string
	Server              serverInfo
	Details             connectionInfoDetails
	CheckedAt           time.Time
	Duration            time.Duration
	ConnectionUpdatedAt time.Time
	Cached              bool
}

type probeOptions struct {
	// Context carries the caller's cancellation (a request or a batch budget).
	Context gocontext.Context
	// ConnectionContext carries the DB and cluster access hydration needs.
	ConnectionContext dbcontext.Context
	Connection        *models.Connection
	// Force bypasses both the health cache and the env-value cache, so a probe
	// re-reads rotated secrets instead of serving a stale verdict.
	Force bool
}

// probeConnectionHealth returns the connection's health, serving a warm cache
// entry unless Force is set. Concurrent probes of the same connection collapse
// into one, so a fleet-wide check from two tabs does not double-dial.
func probeConnectionHealth(options probeOptions) connectionHealthResult {
	id := options.Connection.ID.String()
	if !options.Force {
		if cached, ok := cachedConnectionHealth(id, options.Connection.UpdatedAt); ok {
			return cached
		}
	}
	value, _, _ := connectionHealthGroup.Do(id, func() (any, error) {
		return runConnectionProbe(options), nil
	})
	return value.(connectionHealthResult)
}

func runConnectionProbe(options probeOptions) connectionHealthResult {
	started := time.Now()
	raw := options.Connection
	result := connectionHealthResult{
		ID:                  raw.ID.String(),
		ConnectionUpdatedAt: raw.UpdatedAt,
		CheckedAt:           started.UTC(),
	}

	probeContext, cancel := options.ConnectionContext.Wrap(options.Context).WithTimeout(connectionProbeTimeout)
	defer cancel()

	if options.Force {
		if err := dbcontext.InvalidateConnectionSecrets(probeContext, raw); err != nil {
			probeContext.Logger.V(3).Infof("connection %s: invalidate secret cache: %s", raw.Name, err)
		}
	}

	resolved := cloneConnection(raw)
	if _, err := dbcontext.HydrateConnection(probeContext, resolved); err != nil {
		result.Detail = sanitizeConnectionError(err, raw, resolved)
		result.State = connectionHealthCredentials
		result.Server = serverInfo{Status: "error", Message: result.Detail}
		result.Details = connectionDetails(raw, resolved)
		return finishProbe(probeContext, result, started)
	}
	result.Details = connectionDetails(raw, resolved)

	result.Server = discoverServer(probeContext, probeContext, resolved)
	switch result.Server.Status {
	case "error":
		result.Server.Message = sanitizeConnectionError(fmt.Errorf("%s", result.Server.Message), raw, resolved)
		result.State, result.Detail = connectionHealthUnreachable, result.Server.Message
	case "unavailable":
		result.State, result.Detail = reachabilityHealth(probeContext, raw, resolved)
	default:
		result.State = connectionHealthHealthy
		result.Detail = strings.TrimSpace(result.Server.Product + " " + result.Server.Version)
		if result.Detail == "" {
			result.Detail = "Available"
		}
	}
	return finishProbe(probeContext, result, started)
}

// reachabilityHealth is the fallback for the connection types discoverServer has
// no version query for — most of them. Without it an http, loki or jaeger
// connection could never report anything but "not verifiable".
func reachabilityHealth(
	ctx dbcontext.Context,
	raw, resolved *models.Connection,
) (connectionHealthState, string) {
	if strings.TrimSpace(resolved.URL) == "" && resolved.Type != models.ConnectionTypeOpenTelemetry {
		return connectionHealthUnverifiable, "No endpoint to probe for this connection type"
	}
	probe := testConnection(ctx, resolved)
	detail := sanitizeConnectionError(fmt.Errorf("%s", probe.Message), raw, resolved)
	if probe.OK {
		return connectionHealthHealthy, detail
	}
	return connectionHealthUnreachable, detail
}

func finishProbe(
	ctx dbcontext.Context,
	result connectionHealthResult,
	started time.Time,
) connectionHealthResult {
	result.Duration = time.Since(started)
	storeConnectionHealth(ctx, result)
	return result
}

func connectionDetails(raw, resolved *models.Connection) connectionInfoDetails {
	return connectionInfoDetails{
		Name:               raw.Name,
		Type:               raw.Type,
		Namespace:          raw.Namespace,
		ConfiguredEndpoint: redactConnectionURL(raw.URL),
		ResolvedEndpoint:   redactConnectionURL(resolved.URL),
		ConfiguredUsername: raw.Username,
		ResolvedUsername:   resolved.Username,
		Password: connectionPresence{
			Configured: strings.TrimSpace(raw.Password) != "",
			Resolved:   strings.TrimSpace(resolved.Password) != "",
		},
		Certificate: connectionPresence{
			Configured: strings.TrimSpace(raw.Certificate) != "",
			Resolved:   strings.TrimSpace(resolved.Certificate) != "",
		},
	}
}

func cachedConnectionHealth(id string, updatedAt time.Time) (connectionHealthResult, bool) {
	value, found := connectionHealthCache.Get(id)
	if !found {
		return connectionHealthResult{}, false
	}
	result, ok := value.(connectionHealthResult)
	if !ok || !result.ConnectionUpdatedAt.Equal(updatedAt) {
		return connectionHealthResult{}, false
	}
	result.Cached = true
	return result, true
}

func storeConnectionHealth(ctx dbcontext.Context, result connectionHealthResult) {
	if result.ID == "" || result.State == connectionHealthUnknown {
		return
	}
	connectionHealthCache.Set(
		result.ID, result,
		ctx.Properties().Duration("connections.health.cache.ttl", connectionHealthTTL),
	)
}

func forgetConnectionHealth(id string) {
	connectionHealthCache.Delete(id)
}

// connectionInfoFromHealth projects a probe onto the connection info header's
// shape. Credentials failures are reported by the handler as 422, so this is
// only ever the success projection.
func connectionInfoFromHealth(result connectionHealthResult) connectionInfo {
	return connectionInfo{
		Connection:   result.Details,
		Server:       result.Server,
		DiscoveredAt: result.CheckedAt,
		Cached:       result.Cached,
	}
}

func healthSummary(result connectionHealthResult) *connectionDashboardHealth {
	return &connectionDashboardHealth{
		State:     result.State,
		Detail:    result.Detail,
		CheckedAt: result.CheckedAt,
		Cached:    result.Cached,
	}
}

// connectionHealthHandler serves POST {prefix}/connections/health, the explicit
// opt-in trigger. Listing connections never probes; this is the only batch entry
// point, and the connection info header is the only single-connection one.
type connectionHealthHandler struct {
	path string
	ctx  dbcontext.Context
	next http.Handler
}

func newConnectionHealthHandler(prefix string, ctx dbcontext.Context, next http.Handler) http.Handler {
	return &connectionHealthHandler{
		path: strings.TrimRight(prefix, "/") + "/connections/health",
		ctx:  ctx,
		next: next,
	}
}

type connectionHealthRequest struct {
	IDs   []string `json:"ids"`
	Force bool     `json:"force"`
}

type connectionHealthItem struct {
	ID         string                `json:"id"`
	State      connectionHealthState `json:"state"`
	Detail     string                `json:"detail"`
	CheckedAt  time.Time             `json:"checkedAt"`
	DurationMS int64                 `json:"durationMs"`
	Cached     bool                  `json:"cached"`
}

type connectionHealthResponse struct {
	Results     []connectionHealthItem `json:"results"`
	GeneratedAt time.Time              `json:"generatedAt"`
}

func (h *connectionHealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != h.path {
		h.next.ServeHTTP(w, r)
		return
	}

	var request connectionHealthRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, fmt.Sprintf("decode request body: %v", err), http.StatusBadRequest)
		return
	}
	if len(request.IDs) == 0 {
		http.Error(w, "ids is required", http.StatusBadRequest)
		return
	}
	if len(request.IDs) > maxHealthBatch {
		http.Error(w, fmt.Sprintf("ids exceeds the %d connection batch limit", maxHealthBatch), http.StatusBadRequest)
		return
	}
	connections, err := listConnectionsByIDs(h.ctx.DB().WithContext(r.Context()), request.IDs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, connectionHealthResponse{
		Results:     h.probeAll(r.Context(), connections, request.Force),
		GeneratedAt: time.Now().UTC(),
	})
}

// probeAll runs the batch under a shared budget. The errgroup is deliberately
// not an errgroup.WithContext and every goroutine returns nil: one unreachable
// connection must never cancel its siblings or fail the request. Probes the
// budget outruns report "unknown" rather than erroring.
func (h *connectionHealthHandler) probeAll(
	ctx gocontext.Context,
	connections []*models.Connection,
	force bool,
) []connectionHealthItem {
	batchContext, cancel := gocontext.WithTimeout(ctx, connectionHealthBatchTimeout)
	defer cancel()

	items := make([]connectionHealthItem, len(connections))
	var group errgroup.Group
	group.SetLimit(connectionProbeConcurrency)
	for index, connection := range connections {
		items[index] = connectionHealthItem{
			ID: connection.ID.String(), State: connectionHealthUnknown,
			Detail: "Health check did not complete within the batch budget",
		}
		group.Go(func() error {
			result := probeConnectionHealth(probeOptions{
				Context: batchContext, ConnectionContext: h.ctx, Connection: connection, Force: force,
			})
			items[index] = connectionHealthItem{
				ID: result.ID, State: result.State, Detail: result.Detail,
				CheckedAt: result.CheckedAt, DurationMS: result.Duration.Milliseconds(),
				Cached: result.Cached,
			}
			return nil
		})
	}
	_ = group.Wait()
	return items
}

// listConnectionsByIDs fails fast on an unknown id: the UI always sources ids
// from the inventory response, so a miss is a programming error rather than a
// condition to paper over.
func listConnectionsByIDs(db *gorm.DB, ids []string) ([]*models.Connection, error) {
	var connections []*models.Connection
	if err := db.Model(&models.Connection{}).Where("id IN ?", ids).Order("name").Find(&connections).Error; err != nil {
		return nil, fmt.Errorf("list connections for health check: %w", err)
	}
	found := make(map[string]struct{}, len(connections))
	for _, connection := range connections {
		found[connection.ID.String()] = struct{}{}
	}
	var missing []string
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("unknown connection ids: %s", strings.Join(missing, ", "))
	}
	return connections, nil
}
