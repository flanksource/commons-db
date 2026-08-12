package query

import (
	"fmt"
	"time"
)

// Profile is a declarative, CEL-driven view over a data provider. It names the
// backend to read from, the provider-native query, the output columns (with
// optional CEL formatting), post-query processors, and named context objects.
//
// A Profile is the unifying abstraction across legacy "trace profiles", duty
// View specs, and ad-hoc reports.
type Profile struct {
	// Name identifies the Profile (e.g. "SQL Server trace").
	Name string `json:"profile" yaml:"profile"`

	// Virtual profiles are generated runtime views over temporary data. They use
	// the normal profile execution surfaces but cannot be persisted.
	Virtual   bool       `json:"virtual,omitempty" yaml:"-"`
	ReadOnly  bool       `json:"read_only,omitempty" yaml:"-"`
	ExpiresAt *time.Time `json:"expires_at,omitempty" yaml:"-"`

	// Imports names profiles to merge from left to right before this profile is
	// executed. The authored profile remains unchanged in the profile store.
	Imports []string `json:"imports,omitempty" yaml:"imports,omitempty"`

	// Namespace scopes Kubernetes secret/configmap lookups and workload URLs used
	// by inline provider connections. When empty, the caller's namespace is used.
	Namespace string `json:"namespace,omitempty" yaml:"namespace,omitempty"`

	// Icon overrides the glyph the UI shows for this Profile. It is an opaque
	// icon name resolved by the frontend's icon provider (e.g. "kubernetes",
	// "activemq"). When empty the provider type's own mark is used, so this only
	// needs setting where the backend type is not what the Profile is *about*.
	Icon string `json:"icon,omitempty" yaml:"icon,omitempty"`

	// Provider selects and configures the backend the Profile reads from.
	Provider ProviderConfig `json:"provider" yaml:"provider"`

	// Query is the provider-native query (SQL, PromQL, HTTP path, etc.). It may
	// reference declared params as `{{.params.<name>}}` (or `$(...)`), which are
	// rendered before the provider runs.
	Query string `json:"query,omitempty" yaml:"query,omitempty"`

	// Params declares the server-side filter parameters the Profile accepts. Their
	// resolved values are templated into Query (and context sub-queries) and drive
	// the per-profile FilterBar schema.
	Params []ParamDef `json:"params,omitempty" yaml:"params,omitempty"`

	// Columns declares the output columns in display order. When empty, the
	// provider's raw row keys are used.
	Columns []ColumnDef `json:"columns,omitempty" yaml:"columns,omitempty"`

	// Aliases are ordered CEL projections applied to each provider row. Later
	// aliases can reference values produced by earlier aliases.
	Aliases []AliasDef `json:"aliases,omitempty" yaml:"aliases,omitempty"`

	// Ignore removes provider fields after aliases have been evaluated.
	Ignore []string `json:"ignore,omitempty" yaml:"ignore,omitempty"`

	// Filters are named row predicates evaluated after aliases, dropping rows
	// before they reach the columns. Hidden filters always apply; the rest are
	// togglable and inert until named in FilterEnabledParam.
	Filters []FilterDef `json:"filters,omitempty" yaml:"filters,omitempty"`

	// Processors are post-query steps (e.g. sqlite merge/recon) applied in order.
	Processors []ProcessorSpec `json:"processors,omitempty" yaml:"processors,omitempty"`

	// Context defines secondary queries whose single result becomes a named side
	// object on the Result (e.g. Policy, Plan, Integrations).
	Context map[string]SubQuery `json:"context,omitempty" yaml:"context,omitempty"`

	// Output lists the render targets (e.g. table, html, xlsx, json).
	Output []string `json:"output,omitempty" yaml:"output,omitempty"`

	// Render selects how the frontend presents the result. "table" (the default,
	// when empty) uses the generic data table; "logs" maps the columns onto the
	// canonical LogsTable view (timestamp/level/pod/logger/thread/message, plus an
	// optional duration column) for trace/log profiles. Filtering stays server-side
	// via Params regardless of render mode.
	Render string `json:"render,omitempty" yaml:"render,omitempty"`

	// Trace declares the Profile as a long-running streaming session with
	// explicit setup/teardown; the provider must implement StreamProvider.
	// Mutually exclusive with Top.
	Trace *TraceSpec `json:"trace,omitempty" yaml:"trace,omitempty"`

	// Top declares the Profile as interval-sampled: the engine re-executes the
	// query per tick and each snapshot replaces the last. Mutually exclusive
	// with Trace.
	Top *TopSpec `json:"top,omitempty" yaml:"top,omitempty"`

	// Replay describes how a single result row is turned back into an outbound
	// HTTP request, so a failed or dropped record can be re-sent to its
	// destination without hand-assembling the call.
	Replay *ReplaySpec `json:"replay,omitempty" yaml:"replay,omitempty"`

	// Reconcile is the profile this one is normally joined against, and how:
	// the shared identity, the event-time column, and the per-side row bound.
	// It supplies the defaults of the `reconcile` action, so the join a profile
	// is habitually checked against is stored with it rather than retyped.
	Reconcile *ReconcileConfig `json:"reconcile,omitempty" yaml:"reconcile,omitempty"`

	// Limits are the row caps this Profile sets for itself: the page it returns
	// by default, the largest page a caller may ask for, and where an all-row
	// export stops. Each unset cap takes its default. None of them is the
	// query's own limit, which is a provider option.
	Limits *RowLimits `json:"limits,omitempty" yaml:"limits,omitempty"`

	// Order is the total order this Profile's rows are returned in, ending in a
	// column declared unique. It is what makes a page identifiable twice
	// running, so paging past the first page requires it: without one, two
	// executions of the same query may interleave rows differently and a second
	// page can repeat or skip rows from the first.
	Order Order `json:"order,omitempty" yaml:"order,omitempty"`
}

// RowLimits resolves this Profile's row caps against the defaults, so callers
// never reach for a default constant themselves.
func (p Profile) RowLimits() RowLimits { return p.Limits.Resolve() }

// Pageable reports whether a caller can ask this Profile for a position past
// its first page, and says why not when it cannot.
//
// It is the same question ExecutePages enforces before it serves an offset or a
// cursor, asked early enough to be answered in a capability declaration: a
// surface that advertises paging it will refuse sends the caller to a page that
// can only fail. Whoever describes the profile — OpenAPI parameters, export
// headers — asks here rather than re-deriving the rule.
func (p Profile) Pageable() error {
	if err := p.Order.Pageable(); err != nil {
		return err
	}
	if SupportsPaging(p.Provider.Type) == 0 {
		return fmt.Errorf("provider %q serves no paging strategy", p.Provider.Type)
	}
	return nil
}

// Streamable reports whether this Profile can be served page by page.
//
// A Top sorts the whole result before its first row is correct, and a processor
// that is not a PageProcessor needs every row for the same reason. Either one
// means a page is cut from a full run of the query rather than read from a
// cursor — which is correct but costs the whole query per page, so callers ask
// rather than assume.
func (p Profile) Streamable() (bool, error) {
	if p.Top != nil {
		return false, nil
	}
	return StreamableProcessors(p.Processors)
}

// AliasDef is an ordered, named CEL projection over a provider row.
type AliasDef struct {
	Name string `json:"name" yaml:"name"`
	CEL  string `json:"cel" yaml:"cel"`
}

// Render values the frontend keys presentation off (x-clicky-render):
// RenderLogs selects the canonical LogsTable; RenderTrace and RenderTop select
// the session-backed live views and are derived from the profile kind when
// Render is not set explicitly.
const (
	RenderLogs  = "logs"
	RenderTrace = "trace"
	RenderTop   = "top"
)

// RenderMode returns the effective render value: the explicit Render when set,
// otherwise the profile kind for trace/top profiles, otherwise empty (generic
// table).
func (p Profile) RenderMode() string {
	if p.Render != "" {
		return p.Render
	}
	switch p.Kind() {
	case KindTrace:
		return RenderTrace
	case KindTop:
		return RenderTop
	default:
		return ""
	}
}

// ParamNameForRole returns the first parameter assigned to role, or fallback
// when the profile uses the built-in transport parameter.
func (p Profile) ParamNameForRole(role ParamRole, fallback string) string {
	for _, param := range p.Params {
		if param.Role == role && param.Name != "" {
			return param.Name
		}
	}
	return fallback
}

// HasParamRoleName reports whether name is a profile-declared transport
// parameter for role.
func (p Profile) HasParamRoleName(role ParamRole, name string) bool {
	return p.ParamNameForRole(role, "") == name && name != ""
}

// ProviderConfig selects a registered Provider and supplies the connection and
// provider-specific options.
type ProviderConfig struct {
	// Type is the registered provider key (e.g. "sql", "http", "prometheus").
	Type string `json:"type" yaml:"type"`

	// Connection references a connection (connection://name) or an inline DSN/URL.
	// It is templated with the resolved params before the provider runs.
	Connection string `json:"connection,omitempty" yaml:"connection,omitempty"`

	// Options carries provider-specific knobs. Every string in it — however
	// deeply nested — is templated with the resolved params before the provider
	// runs, so `{{.params.x}}` and `$(.params.x)` work in any provider's options.
	Options map[string]any `json:"options,omitempty" yaml:"options,omitempty"`
}

// SubQuery is a secondary provider query whose result is attached to the Result
// as a named context object.
type SubQuery struct {
	Provider ProviderConfig `json:"provider" yaml:"provider"`
	Query    string         `json:"query,omitempty" yaml:"query,omitempty"`
}

// ProcessorSpec names a post-query processor and carries its raw config, which
// the processor decodes for itself.
type ProcessorSpec struct {
	// Type is the registered processor key (e.g. "sqlite.merge", "sqlite.recon").
	// Optional when Use names a library entry, which supplies it.
	Type string `json:"type,omitempty" yaml:"type,omitempty"`

	// Use names a library processor (see RegisterNamedProcessor) whose type and
	// configuration this spec starts from — the processor equivalent of
	// Profile.Imports. Config set here is merged over the preset's, so a profile
	// can adopt a shared transform and still override a single key.
	Use string `json:"use,omitempty" yaml:"use,omitempty"`

	// Config is the processor-specific configuration.
	Config map[string]any `json:"config,omitempty" yaml:"config,omitempty"`
}
