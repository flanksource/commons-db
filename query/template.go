package query

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/commons/logger"
	"github.com/flanksource/gomplate/v3"

	"github.com/flanksource/commons-db/context"
)

// paramRefPattern matches the two ways a template reaches a resolved parameter:
// the dotted form `.params.<name>`, and `index .params "<name>"` for a name that
// is not a bare Go identifier.
var paramRefPattern = regexp.MustCompile(`\.params(?:\.([A-Za-z_][A-Za-z0-9_]*)|\s+"([^"]+)")`)

// hasTemplate reports whether s carries either of the delimiter pairs the repo
// templates with. A string without them is returned untouched, so a plain
// option never pays for the template engine.
func hasTemplate(s string) bool {
	return strings.Contains(s, "{{") || strings.Contains(s, "$(")
}

// paramRefs returns the distinct parameter names s references, in the order
// they appear.
func paramRefs(s string) []string {
	var names []string
	seen := map[string]bool{}
	for _, match := range paramRefPattern.FindAllStringSubmatch(s, -1) {
		name := match[1]
		if name == "" {
			name = match[2]
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// paramTemplate interpolates resolved profile parameters into the strings of a
// profile's execution config — its query, its provider options and its
// connection. It records which parameters it consumed, so a provider with its
// own structural parameter binding does not report an interpolated parameter as
// unreferenced.
type paramTemplate struct {
	templater gomplate.StructTemplater
	params    map[string]any
	used      map[string]bool
}

// newParamTemplate builds a templater over params. It borrows
// Context.NewStructTemplater, which already carries both delimiter sets, the
// registered template functions, and value functions.
func newParamTemplate(ctx context.Context, params map[string]any) *paramTemplate {
	templater := ctx.NewStructTemplater(map[string]any{"params": params}, "", nil)
	if templater.Context.Logger == nil {
		// StructTemplater.Template dereferences the logger without the nil guard
		// that Walk has, so a context that was never constructed would panic.
		templater.Context.Logger = logger.StandardLogger()
	}
	return &paramTemplate{templater: templater, params: params, used: map[string]bool{}}
}

func providerTemplateParams(cfg ProviderConfig, defs []ParamDef, params map[string]any) (map[string]any, error) {
	if !isClickHouseTemplateProvider(cfg) {
		return params, nil
	}
	var rendered map[string]any
	for _, def := range defs {
		if def.Type != ParamTypeDateTime || def.Template != "" {
			continue
		}
		value, ok := params[def.Name]
		if !ok {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, fmt.Sprint(value))
		if err != nil {
			return nil, fmt.Errorf("clickhouse datetime param %q is not RFC3339: %w", def.Name, err)
		}
		if rendered == nil {
			rendered = maps.Clone(params)
		}
		rendered[def.Name] = parsed.UTC().Format("2006-01-02 15:04:05.999999999")
	}
	if rendered == nil {
		return params, nil
	}
	return rendered, nil
}

func isClickHouseTemplateProvider(cfg ProviderConfig) bool {
	if strings.EqualFold(cfg.Type, "clickhouse") {
		return true
	}
	driver, _ := cfg.Options["driver"].(string)
	return strings.EqualFold(cfg.Type, "sql") && strings.EqualFold(driver, "clickhouse")
}

// render templates one string. where names the field for the error message.
//
// Every parameter the template references must have a resolved value: a Go
// template renders a missing key as "<no value>", which would reach the backend
// as a silently wrong query rather than a failure.
func (t *paramTemplate) render(where, s string) (string, error) {
	if !hasTemplate(s) {
		return s, nil
	}
	for _, name := range paramRefs(s) {
		if _, ok := t.params[name]; !ok {
			return "", fmt.Errorf("%s references param %q, which has no value", where, name)
		}
		t.used[name] = true
	}
	rendered, err := t.templater.Template(s)
	if err != nil {
		return "", fmt.Errorf("%s: %w", where, err)
	}
	return rendered, nil
}

// renderAny walks maps and slices and renders every string it reaches. Map keys
// are left alone and non-string scalars keep their Go type, so a decoded options
// map comes back with the shape it went in with.
func (t *paramTemplate) renderAny(where string, v any) (any, error) {
	switch typed := v.(type) {
	case string:
		return t.render(where, typed)
	case map[string]any:
		if len(typed) == 0 {
			return typed, nil
		}
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			rendered, err := t.renderAny(where+"."+key, value)
			if err != nil {
				return nil, err
			}
			out[key] = rendered
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, value := range typed {
			rendered, err := t.renderAny(fmt.Sprintf("%s[%d]", where, i), value)
			if err != nil {
				return nil, err
			}
			out[i] = rendered
		}
		return out, nil
	case []string:
		out := make([]string, len(typed))
		for i, value := range typed {
			rendered, err := t.render(fmt.Sprintf("%s[%d]", where, i), value)
			if err != nil {
				return nil, err
			}
			out[i] = rendered
		}
		return out, nil
	default:
		return v, nil
	}
}

// renderOptions renders a provider options map, returning nil when there is
// nothing to render so the request carries the profile's own map unchanged.
func (t *paramTemplate) renderOptions(options map[string]any) (map[string]any, error) {
	if len(options) == 0 {
		return options, nil
	}
	rendered, err := t.renderAny("provider.options", options)
	if err != nil {
		return nil, err
	}
	return rendered.(map[string]any), nil
}

// usedParams returns the parameters consumed by the strings rendered so far,
// sorted so the result is stable.
func (t *paramTemplate) usedParams() []string {
	if len(t.used) == 0 {
		return nil
	}
	names := make([]string, 0, len(t.used))
	for name := range t.used {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// RenderParamsJSON templates every string in a JSON document with the resolved
// parameters under `params`, and reports which parameters it consumed. Object
// keys and non-string scalars are left alone. It is how a caller holding a JSON
// body — the query builder's compile endpoint, for instance — reaches the same
// interpolation a profile gets at execution time.
func RenderParamsJSON(ctx context.Context, doc []byte, params map[string]any) ([]byte, []string, error) {
	if !hasTemplate(string(doc)) {
		return doc, nil, nil
	}
	var decoded any
	if err := json.Unmarshal(doc, &decoded); err != nil {
		return nil, nil, fmt.Errorf("decode document to template: %w", err)
	}
	template := newParamTemplate(ctx, params)
	rendered, err := template.renderAny("$", decoded)
	if err != nil {
		return nil, nil, err
	}
	encoded, err := json.Marshal(rendered)
	if err != nil {
		return nil, nil, fmt.Errorf("encode templated document: %w", err)
	}
	return encoded, template.usedParams(), nil
}
