package query

// Profile is a declarative, CEL-driven view over a data provider. It names the
// backend to read from, the provider-native query, the output columns (with
// optional CEL formatting), post-query processors, and named context objects.
//
// A Profile is the unifying abstraction across legacy "trace profiles", duty
// View specs, and ad-hoc reports.
type Profile struct {
	// Name identifies the Profile (e.g. "SQL Server trace").
	Name string `json:"profile" yaml:"profile"`

	// Namespace scopes Kubernetes secret/configmap lookups and workload URLs used
	// by inline provider connections. When empty, the caller's namespace is used.
	Namespace string `json:"namespace,omitempty" yaml:"namespace,omitempty"`

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
	Connection string `json:"connection,omitempty" yaml:"connection,omitempty"`

	// Options carries provider-specific knobs.
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
	Type string `json:"type" yaml:"type"`

	// Config is the processor-specific configuration.
	Config map[string]any `json:"config,omitempty" yaml:"config,omitempty"`
}
