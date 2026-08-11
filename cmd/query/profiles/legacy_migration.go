package profiles

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/flanksource/commons-db/query"
	yamlv3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"
)

const legacyTraceProvider = "legacy-trace"

type legacyTraceParam struct {
	Field    string `json:"field,omitempty" yaml:"field,omitempty"`
	Operator string `json:"operator,omitempty" yaml:"operator,omitempty"`

	// Format is parsed so an old file still loads, but it is not migrated: no
	// version of the trace query builder ever read it.
	Format string `json:"format,omitempty" yaml:"format,omitempty"`

	Template    string `json:"template,omitempty" yaml:"template,omitempty"`
	Clause      string `json:"clause,omitempty" yaml:"clause,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Internal    bool   `json:"internal,omitempty" yaml:"internal,omitempty"`
	Required    bool   `json:"required,omitempty" yaml:"required,omitempty"`
}

type legacyTraceColumn struct {
	Name   string `json:"name" yaml:"name"`
	Field  string `json:"field" yaml:"field"`
	Style  string `json:"style,omitempty" yaml:"style,omitempty"`
	Detail bool   `json:"detail,omitempty" yaml:"detail,omitempty"`
}

type legacyTraceProfile struct {
	Name           string                      `json:"name" yaml:"name"`
	Kind           string                      `json:"kind,omitempty" yaml:"kind,omitempty"`
	Format         string                      `json:"format,omitempty" yaml:"format,omitempty"`
	Index          string                      `json:"index,omitempty" yaml:"index,omitempty"`
	DateField      string                      `json:"dateField,omitempty" yaml:"dateField,omitempty"`
	TraceIDField   string                      `json:"traceIdField,omitempty" yaml:"traceIdField,omitempty"`
	SpanIDField    string                      `json:"spanIdField,omitempty" yaml:"spanIdField,omitempty"`
	ParentIDField  string                      `json:"parentIdField,omitempty" yaml:"parentIdField,omitempty"`
	ParentRefType  string                      `json:"parentRefType,omitempty" yaml:"parentRefType,omitempty"`
	ServiceField   string                      `json:"serviceField,omitempty" yaml:"serviceField,omitempty"`
	OperationField string                      `json:"operationField,omitempty" yaml:"operationField,omitempty"`
	StatusFields   []string                    `json:"statusFields,omitempty" yaml:"statusFields,omitempty"`
	SelectFields   []string                    `json:"selectFields,omitempty" yaml:"selectFields,omitempty"`
	SourceExcludes []string                    `json:"sourceExcludes,omitempty" yaml:"sourceExcludes,omitempty"`
	Imports        []string                    `json:"imports,omitempty" yaml:"imports,omitempty"`
	Defaults       map[string]any              `json:"defaults,omitempty" yaml:"defaults,omitempty"`
	Params         map[string]legacyTraceParam `json:"params,omitempty" yaml:"params,omitempty"`
	Aliases        orderedLegacyAliases        `json:"-" yaml:"aliases,omitempty"`
	Ignore         []string                    `json:"ignore,omitempty" yaml:"ignore,omitempty"`
	Columns        []legacyTraceColumn         `json:"columns,omitempty" yaml:"columns,omitempty"`
	Filters        []query.FilterDef           `json:"filters,omitempty" yaml:"filters,omitempty"`
	SQL            map[string]any              `json:"sql,omitempty" yaml:"sql,omitempty"`
	Kubernetes     map[string]any              `json:"kubernetes,omitempty" yaml:"kubernetes,omitempty"`
	Arthas         map[string]any              `json:"arthas,omitempty" yaml:"arthas,omitempty"`
	Replay         map[string]any              `json:"replay,omitempty" yaml:"replay,omitempty"`
}

type orderedLegacyAliases []query.AliasDef

func (a *orderedLegacyAliases) UnmarshalYAML(node *yamlv3.Node) error {
	if node.Kind != yamlv3.MappingNode {
		return fmt.Errorf("aliases must be a mapping")
	}
	for index := 0; index < len(node.Content); index += 2 {
		name, spec := node.Content[index].Value, node.Content[index+1]
		// An alias is written either as a bare expression or as a mapping with a
		// cel key. Both forms are in the wild, and the shorthand is the common
		// one, so a profile using it must not be read as malformed.
		if spec.Kind == yamlv3.ScalarNode {
			*a = append(*a, query.AliasDef{Name: name, CEL: spec.Value})
			continue
		}
		var value struct {
			CEL string `yaml:"cel"`
		}
		if err := spec.Decode(&value); err != nil {
			return fmt.Errorf("alias %q: %w", name, err)
		}
		*a = append(*a, query.AliasDef{Name: name, CEL: value.CEL})
	}
	return nil
}

type profileMigration struct {
	path string
	data []byte
}

func (s *FileStore) migrateLegacyTraceProfiles() error {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return fmt.Errorf("read profiles dir %q for migration: %w", s.Dir, err)
	}
	migrations := make([]profileMigration, 0)
	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}
		path := filepath.Join(s.Dir, entry.Name())
		migration, ok, err := legacyProfileMigration(path)
		if err != nil {
			return err
		}
		if ok {
			migrations = append(migrations, migration)
		}
	}
	for _, migration := range migrations {
		if err := replaceProfileFile(migration); err != nil {
			return err
		}
	}
	return nil
}

func legacyProfileMigration(path string) (profileMigration, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return profileMigration{}, false, fmt.Errorf("read profile %q for migration: %w", path, err)
	}
	var identity struct {
		Profile  string               `json:"profile"`
		Name     string               `json:"name"`
		Provider query.ProviderConfig `json:"provider"`
	}
	if err := yaml.Unmarshal(data, &identity); err != nil {
		return profileMigration{}, false, fmt.Errorf("parse profile %q for migration: %w", path, err)
	}
	if identity.Profile != "" {
		if identity.Provider.Type != legacyTraceProvider {
			return profileMigration{}, false, nil
		}
		var current query.Profile
		if err := yaml.Unmarshal(data, &current); err != nil {
			return profileMigration{}, false, fmt.Errorf("parse interim trace profile %q: %w", path, err)
		}
		source, _ := current.Provider.Options["source"].(string)
		kind, _ := current.Provider.Options["kind"].(string)
		if source == "" || kind != "opensearch" && kind != "import" {
			return profileMigration{}, false, nil
		}
		converted, err := convertLegacyTraceSource(source)
		if err != nil {
			return profileMigration{}, false, fmt.Errorf("convert interim trace profile %q: %w", path, err)
		}
		converted.Provider.Connection = current.Provider.Connection
		data, err := yaml.Marshal(converted)
		if err != nil {
			return profileMigration{}, false, fmt.Errorf("marshal migrated trace profile %q: %w", path, err)
		}
		return profileMigration{path: path, data: data}, true, nil
	}
	if identity.Name == "" {
		return profileMigration{}, false, nil
	}
	profile, err := convertLegacyTraceSource(string(data))
	if err != nil {
		return profileMigration{}, false, fmt.Errorf("convert legacy trace profile %q: %w", path, err)
	}
	converted, err := yaml.Marshal(profile)
	if err != nil {
		return profileMigration{}, false, fmt.Errorf("marshal migrated trace profile %q: %w", path, err)
	}
	return profileMigration{path: path, data: converted}, true, nil
}

// ConvertLegacyProfile turns one legacy trace-profile document into a Profile.
//
// FileStore migrates a directory in place, which is the right shape for user
// files on disk. An application whose built-in profiles are embedded has no
// directory to migrate and needs the same mapping applied at load time, so the
// conversion is available on its own rather than only as a side effect of
// opening a store.
func ConvertLegacyProfile(source []byte) (query.Profile, error) {
	return convertLegacyTraceSource(string(source))
}

func convertLegacyTraceSource(source string) (query.Profile, error) {
	var legacy legacyTraceProfile
	if err := yamlv3.Unmarshal([]byte(source), &legacy); err != nil {
		return query.Profile{}, fmt.Errorf("parse legacy trace source: %w", err)
	}
	return convertLegacyTraceProfile(legacy, source)
}

func convertLegacyTraceProfile(legacy legacyTraceProfile, source string) (query.Profile, error) {
	if strings.TrimSpace(legacy.Name) == "" {
		return query.Profile{}, fmt.Errorf("name is required")
	}
	params := make([]query.ParamDef, 0, len(legacy.Params))
	for _, name := range sortedKeys(legacy.Params) {
		param := legacy.Params[name]
		def := query.ParamDef{
			Name: name, Description: param.Description, Required: param.Required, Template: param.Template,
		}
		if value, ok := legacy.Defaults[name]; ok {
			def.Default = value
			def.Type = legacyParamType(value)
		}
		params = append(params, def)
	}
	columns := make([]query.ColumnDef, len(legacy.Columns))
	for i, column := range legacy.Columns {
		columns[i] = query.ColumnDef{
			Name: column.Name, CEL: column.Field, Hidden: column.Detail, Style: column.Style,
		}
	}
	provider := query.ProviderConfig{Type: legacyTraceProvider, Options: map[string]any{
		"kind": legacyTraceKind(legacy), "source": source,
	}}
	kind := legacyTraceKind(legacy)
	if kind == "kubernetes" {
		options, err := legacyKubernetesOptions(legacy)
		if err != nil {
			return query.Profile{}, err
		}
		provider = query.ProviderConfig{Type: "k8s", Options: options}
		params = ensureParam(params, query.ParamDef{
			Name: "namespace", Description: "Kubernetes namespace to read logs from",
		})
	}
	if kind == "opensearch" || kind == "import" {
		search, err := legacyOpenTelemetrySearch(legacy.Params)
		if err != nil {
			return query.Profile{}, err
		}
		options := map[string]any{}
		// An import inherits every index and field setting from its parent, so
		// only its own filters travel with it.
		if kind == "opensearch" {
			options = legacyOpenTelemetryOptions(legacy)
		}
		if search != nil {
			options["search"] = search
		}
		provider = query.ProviderConfig{Type: "opentelemetry", Options: options}
	}
	return query.Profile{
		Name: legacy.Name, Imports: legacy.Imports, Provider: provider,
		Params: params, Columns: columns, Aliases: []query.AliasDef(legacy.Aliases), Ignore: legacy.Ignore,
		Filters: legacy.Filters,
	}, nil
}

// ensureParam appends def unless the profile already declares that name, so a
// parameter the conversion needs never shadows one the author wrote.
func ensureParam(params []query.ParamDef, def query.ParamDef) []query.ParamDef {
	for _, existing := range params {
		if existing.Name == def.Name {
			return params
		}
	}
	return append(params, def)
}

// legacyKubernetesWorkloadKinds maps the short workload references a legacy
// `kubernetes.target` uses onto the kinds the k8s log provider accepts.
var legacyKubernetesWorkloadKinds = map[string]string{
	"po": "Pod", "pod": "Pod", "pods": "Pod",
	"deploy": "Deployment", "deployment": "Deployment", "deployments": "Deployment",
	"sts": "StatefulSet", "statefulset": "StatefulSet", "statefulsets": "StatefulSet",
	"ds": "DaemonSet", "daemonset": "DaemonSet", "daemonsets": "DaemonSet",
}

// legacyKubernetesOptions maps a legacy `kubernetes:` block onto the k8s log
// provider's options.
//
// A base profile carries only defaults and no target — it exists to be imported
// — so an absent target is not an error here; the provider rejects a profile
// that is actually run without one.
func legacyKubernetesOptions(legacy legacyTraceProfile) (map[string]any, error) {
	options := map[string]any{}
	if target, _ := legacy.Kubernetes["target"].(string); target != "" {
		shorthand, name, found := strings.Cut(target, "/")
		if !found || name == "" {
			return nil, fmt.Errorf("kubernetes.target %q is not a kind/name reference", target)
		}
		kind, ok := legacyKubernetesWorkloadKinds[strings.ToLower(shorthand)]
		if !ok {
			return nil, fmt.Errorf("kubernetes.target %q has an unsupported kind %q", target, shorthand)
		}
		options["kind"], options["name"] = kind, name
	}
	// The legacy reader resolved an empty namespace from the active kubecontext.
	// The provider requires one, so it becomes a parameter the caller supplies
	// rather than an ambient value the profile silently inherits.
	if namespace, _ := legacy.Kubernetes["namespace"].(string); namespace != "" {
		options["namespace"] = namespace
	} else {
		options["namespace"] = "{{.params.namespace}}"
	}
	if since, _ := legacy.Kubernetes["since"].(string); since != "" {
		options["start"] = "now-" + since
	}
	if tail, ok := legacy.Kubernetes["tail"]; ok {
		options["limit"] = fmt.Sprintf("%v", tail)
	}
	// Fail loudly on the settings that have no home, rather than dropping them
	// and quietly widening what the profile reads.
	for _, unsupported := range []string{"selector", "container", "grep", "previous"} {
		if value, ok := legacy.Kubernetes[unsupported]; ok && value != nil && value != "" {
			return nil, fmt.Errorf("kubernetes.%s is not supported by the k8s provider", unsupported)
		}
	}
	return options, nil
}

func legacyOpenTelemetryOptions(legacy legacyTraceProfile) map[string]any {
	options := map[string]any{
		"format": legacy.Format, "index": legacy.Index, "dateField": legacy.DateField,
		"traceIdField": legacy.TraceIDField, "spanIdField": legacy.SpanIDField,
		"parentIdField": legacy.ParentIDField, "parentRefType": legacy.ParentRefType,
		"serviceField": legacy.ServiceField, "operationField": legacy.OperationField,
		"statusFields": legacy.StatusFields, "selectFields": legacy.SelectFields,
		"sourceExcludes": legacy.SourceExcludes,
	}
	for key, value := range options {
		switch typed := value.(type) {
		case string:
			if typed == "" {
				delete(options, key)
			}
		case []string:
			if len(typed) == 0 {
				delete(options, key)
			}
		case map[string]any:
			if len(typed) == 0 {
				delete(options, key)
			}
		}
	}
	return options
}

func legacyTraceKind(profile legacyTraceProfile) string {
	if profile.Kind != "" {
		return profile.Kind
	}
	switch {
	case profile.SQL != nil:
		return "sql"
	case profile.Kubernetes != nil:
		return "kubernetes"
	case profile.Arthas != nil:
		return "arthas"
	case profile.Index != "" || profile.Format != "":
		return "opensearch"
	case profile.Replay != nil:
		return "replay"
	case len(profile.Imports) > 0:
		return "import"
	default:
		return "unknown"
	}
}

func legacyParamType(value any) query.ParamType {
	switch value.(type) {
	case bool:
		return query.ParamTypeBoolean
	case float32, float64, int, int32, int64:
		return query.ParamTypeNumber
	default:
		return query.ParamTypeString
	}
}

func replaceProfileFile(migration profileMigration) error {
	temporary := migration.path + ".migrating"
	if err := os.WriteFile(temporary, migration.data, 0o600); err != nil {
		return fmt.Errorf("write migrated profile %q: %w", migration.path, err)
	}
	if err := os.Rename(temporary, migration.path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("replace migrated profile %q: %w", migration.path, err)
	}
	return nil
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
